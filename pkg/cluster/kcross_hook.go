// kcross 隧道接入挂钩：绑定表命中的集群经 kcross Gateway CONNECT 隧道
// 访问 apiserver。本文件为 cluster 包内的集成胶水，尽量不侵入原有文件——
// cluster_manager.go 仅保留分支调用、在线门控与指纹比对三处小改
package cluster

import (
	"context"
	"errors"

	"github.com/zxh326/kite/pkg/kcross"
	"github.com/zxh326/kite/pkg/model"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
)

// errKCrossDisabled 集群有绑定但网关配置缺失/未启用
var errKCrossDisabled = errors.New("kcross integration is not configured (check gateway url/token in kcross settings)")

func init() {
	// 集成配置/绑定变更后触发集群重同步；注入回调避免 kcross → cluster 反向依赖
	kcross.OnChange = TriggerClusterSync
}

// buildKCrossClientSet 经 kcross 隧道构建 ClientSet：kubeconfig 决定身份与
// TLS（Host 保留真实 apiserver 地址，SNI/CA 端到端校验），仅注入 Dial；
// Prometheus 走 newClientSet 内既有的 Dial 泛化路径
func buildKCrossClientSet(cluster *model.Cluster, binding *model.KcrossBinding) (*ClientSet, error) {
	s, err := kcross.GetSettings()
	if err != nil {
		return nil, err
	}
	if !s.Enabled {
		return nil, errKCrossDisabled
	}

	restConfig, err := clientcmd.RESTConfigFromKubeConfig([]byte(cluster.Config))
	if err != nil {
		klog.Warningf("Failed to create REST config for cluster %s: %v", cluster.Name, err)
		return nil, err
	}
	restConfig.Dial = (&kcross.Dialer{
		GatewayURL: s.GatewayURL,
		Token:      s.Token,
		PeerID:     binding.PeerID,
		Capability: binding.Capability,
	}).DialContext
	// 隧道目标由网关路由，禁止环境代理变量劫持
	restConfig.Proxy = nil

	cs, err := newClientSet(cluster.Name, restConfig, cluster.PrometheusURL)
	if err != nil {
		return nil, err
	}
	cs.config = string(cluster.Config)
	cs.kcrossFingerprint = kcrossFingerprint(s, binding)
	return cs, nil
}

// kcrossFingerprint 网关配置/peer/能力任一变化即需重建 ClientSet
func kcrossFingerprint(s kcross.Settings, binding *model.KcrossBinding) string {
	return kcross.Fingerprint(s.GatewayURL, s.Token, binding.PeerID, binding.Capability)
}

// expectedKCrossFingerprint 集群当前绑定应有的指纹；无绑定时为空串
func expectedKCrossFingerprint(clusterID uint) string {
	binding := model.GetKcrossBindingByCluster(clusterID)
	if binding == nil {
		return ""
	}
	s, err := kcross.GetSettings()
	if err != nil || !s.Enabled {
		return ""
	}
	return kcrossFingerprint(s, binding)
}

// kcrossPeerOnline 绑定集群的 peer 是否在线；未配置/查询失败按离线（fail closed）
func kcrossPeerOnline(clusterID uint) bool {
	binding := model.GetKcrossBindingByCluster(clusterID)
	if binding == nil {
		return false
	}
	return kcross.PeerOnline(context.Background(), binding.PeerID)
}
