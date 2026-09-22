package kcross

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/zxh326/kite/pkg/model"
	"gorm.io/gorm"
)

// OnChange 配置/绑定变更后的回调；由 cluster 包注入（触发集群重同步），
// 避免 kcross → cluster 的反向依赖
var OnChange func()

func triggerClusterSync() {
	if OnChange != nil {
		OnChange()
	}
}

// RegisterRoutes 挂载 kcross 集成端点（admin 权限组内调用）：
// setting 配置/连通测试、discover 节点发现、bindings 集群绑定
func RegisterRoutes(group *gin.RouterGroup) {
	group.GET("/setting", HandleGetSetting)
	group.PUT("/setting", HandleUpdateSetting)
	group.POST("/setting/test", HandleTestSetting)
	group.GET("/discover", HandleDiscover)
	group.GET("/bindings", HandleListBindings)
	group.POST("/bindings", HandleCreateBinding)
	group.GET("/peers/:peerId/pty", HandlePeerPTY)
}

// HandleGetSetting 读取集成配置（token 不回显，仅返回是否已配置）
func HandleGetSetting(c *gin.Context) {
	s, err := GetSettings()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"enabled":              s.Enabled,
		"gateways":             s.Gateways,
		"adminToken":           "",
		"adminTokenConfigured": s.Token != "",
	})
}

// HandleUpdateSetting 保存集成配置；token 留空表示保留原值
func HandleUpdateSetting(c *gin.Context) {
	var req struct {
		Enabled    *bool   `json:"enabled"`
		Gateways   *string `json:"gateways"`
		AdminToken *string `json:"adminToken"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	current, err := model.GetKcrossSetting()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	gateways := current.GatewayURL
	if req.Gateways != nil {
		gateways = *req.Gateways
	}
	gatewayList := parseGateways(gateways)
	for _, gw := range gatewayList {
		if !strings.HasPrefix(gw, "http://") && !strings.HasPrefix(gw, "https://") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "each gateway must start with http:// or https://: " + gw})
			return
		}
	}

	adminToken := strings.TrimSpace(string(current.AdminToken))
	shouldUpdateToken := false
	if req.AdminToken != nil {
		incoming := strings.TrimSpace(*req.AdminToken)
		if incoming != "" {
			adminToken = incoming
			shouldUpdateToken = true
		}
	}

	enabled := current.Enabled
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	if enabled && (len(gatewayList) == 0 || adminToken == "") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "gateways and adminToken are required when enabled is true"})
		return
	}

	updates := map[string]interface{}{}
	if req.Enabled != nil {
		updates["enabled"] = enabled
	}
	if req.Gateways != nil {
		updates["gateway_url"] = strings.Join(gatewayList, ",")
	}
	if shouldUpdateToken {
		updates["admin_token"] = model.SecretString(adminToken)
	}

	updated, err := model.UpdateKcrossSetting(updates)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	triggerClusterSync()
	c.JSON(http.StatusOK, gin.H{
		"enabled":              updated.Enabled,
		"gateways":             parseGateways(updated.GatewayURL),
		"adminToken":           "",
		"adminTokenConfigured": strings.TrimSpace(string(updated.AdminToken)) != "",
	})
}

// HandleTestSetting 用当前（或请求体中的待保存）配置试连 Gateway 并返回节点数
func HandleTestSetting(c *gin.Context) {
	var req struct {
		Gateways   *string `json:"gateways"`
		AdminToken *string `json:"adminToken"`
	}
	_ = c.ShouldBindJSON(&req)

	s, err := GetSettings()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if req.Gateways != nil && strings.TrimSpace(*req.Gateways) != "" {
		s.Gateways = parseGateways(*req.Gateways)
	}
	if req.AdminToken != nil && strings.TrimSpace(*req.AdminToken) != "" {
		s.Token = strings.TrimSpace(*req.AdminToken)
	}
	if len(s.Gateways) == 0 || s.Token == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "gateways and adminToken are required"})
		return
	}
	s.Enabled = true

	peers, err := DiscoverWith(c.Request.Context(), s)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "connected", "peerCount": len(peers)})
}

// HandleDiscover 发现节点并标注是否已绑定集群
func HandleDiscover(c *gin.Context) {
	peers, err := Discover(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	bindings, err := model.ListKcrossBindings()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	clusterByPeer := make(map[string]model.KcrossBinding, len(bindings))
	clusterNames := make(map[uint]string)
	for _, b := range bindings {
		clusterByPeer[b.PeerID] = *b
		if cluster, err := model.GetClusterByID(b.ClusterID); err == nil {
			clusterNames[b.ClusterID] = cluster.Name
		}
	}

	type peerResult struct {
		Peer
		Kubernetes  *Capability `json:"kubernetes,omitempty"`
		Prometheus  *Capability `json:"prometheus,omitempty"`
		Bound       bool        `json:"bound"`
		ClusterID   uint        `json:"clusterId,omitempty"`
		ClusterName string      `json:"clusterName,omitempty"`
	}

	result := make([]peerResult, 0, len(peers))
	for _, p := range peers {
		pr := peerResult{Peer: p}
		for i := range p.Capabilities {
			cap := p.Capabilities[i]
			switch cap.Name {
			case DefaultKubernetesCapability:
				pr.Kubernetes = &cap
			case DefaultPrometheusCapability:
				pr.Prometheus = &cap
			}
		}
		if b, ok := clusterByPeer[p.ID]; ok {
			pr.Bound = true
			pr.ClusterID = b.ClusterID
			pr.ClusterName = clusterNames[b.ClusterID]
		}
		result = append(result, pr)
	}
	c.JSON(http.StatusOK, gin.H{"peers": result})
}

// HandleListBindings 全量绑定（前端与集群列表合并展示隧道徽标）
func HandleListBindings(c *gin.Context) {
	bindings, err := model.ListKcrossBindings()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"bindings": bindings})
}

// HandleCreateBinding 集群创建后绑定 peer 能力（upsert）。
// 绑定时生成零凭证 kubeconfig（server = 网关 http 反代路径）写回集群，
// kite 侧不存储任何集群凭证；身份由 peer 按网关断言注入的 SA 决定
func HandleCreateBinding(c *gin.Context) {
	var req struct {
		ClusterID  uint   `json:"clusterId" binding:"required"`
		PeerID     string `json:"peerId" binding:"required"`
		Gateway    string `json:"gateway"`
		Capability string `json:"capability"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Capability == "" {
		req.Capability = DefaultKubernetesCapability
	}

	cluster, err := model.GetClusterByID(req.ClusterID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "cluster not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	gateway := strings.TrimSpace(req.Gateway)
	if gateway == "" {
		// 未显式给网关时按 peer 从发现缓存匹配归属
		if peers, err := Discover(c.Request.Context()); err == nil {
			for _, p := range peers {
				if p.ID == req.PeerID {
					gateway = p.Gateway
					break
				}
			}
		}
	}
	if gateway == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "gateway is required (peer not found in discovery)"})
		return
	}

	kubeconfig := BuildZeroKubeconfig(gateway, req.PeerID, req.Capability)
	if err := model.UpdateCluster(cluster, map[string]interface{}{"config": model.SecretString(kubeconfig)}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	binding, err := model.UpsertKcrossBinding(req.ClusterID, req.PeerID, req.Capability)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	triggerClusterSync()
	c.JSON(http.StatusCreated, binding)
}
