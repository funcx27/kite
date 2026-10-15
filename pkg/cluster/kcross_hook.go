// kcross 隧道接入挂钩：绑定表命中的集群经 kcross 网关 http 代理访问 apiserver。
// 本文件为 cluster 包内的集成胶水，cluster_manager.go 仅保留在线门控一处小改。
//
// 凭证模型（handoff-2）：纳管时由 kcross.BindCluster 生成零凭证 kubeconfig
// （server = http://{gateway}/proxy/{peerID}/{capability}），集群走 kite 原生
// kubeconfig 模式直连网关反代；peer 侧凭网关断言头注入 SA（三段式），kite 不持有
// 任何集群凭证。有效权限 = kite RBAC ∩ tunnel SA RBAC。
package cluster

import (
	"context"

	"github.com/zxh326/kite/pkg/kcross"
	"github.com/zxh326/kite/pkg/model"
)

func init() {
	// 集成配置/绑定变更后触发集群重同步；注入回调避免 kcross → cluster 反向依赖
	kcross.OnChange = TriggerClusterSync
}

// kcrossPeerOnline 绑定集群的 peer 是否在线；未配置/查询失败按离线（fail closed）。
// peer 离线时网关反代会返回 503，这里提前标记让集群列表给出等待提示
func kcrossPeerOnline(clusterID uint) bool {
	binding := model.GetKcrossBindingByCluster(clusterID)
	if binding == nil {
		return false
	}
	return kcross.PeerOnline(context.Background(), binding.PeerID)
}

// kcrossOfflineGate 在线门控：绑定集群的 peer 离线时按“等待接入”处理——摘除已有
// client、按 Enable 置错误态。返回 true 表示本集群本轮跳过（fail closed）。
func kcrossOfflineGate(cm *ClusterManager, cluster *model.Cluster, current *ClientSet, currentExist bool) bool {
	if model.GetKcrossBindingByCluster(cluster.ID) == nil || kcrossPeerOnline(cluster.ID) {
		return false
	}
	if currentExist {
		cm.mu.Lock()
		delete(cm.clusters, cluster.Name)
		cm.mu.Unlock()
		current.K8sClient.Stop(cluster.Name)
	}
	cm.mu.Lock()
	if cluster.Enable {
		cm.errors[cluster.Name] = "waiting for kcross peer connection"
	} else {
		delete(cm.errors, cluster.Name)
	}
	cm.mu.Unlock()
	return true
}
