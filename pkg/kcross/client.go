// Package kcross 对接 kcross Gateway：集群动态发现（admin API）与
// 零凭证纳管（server URL 指向网关 http 反代，peer 侧注入 SA，kite 不持有
// 任何集群凭证）。pkg/kcross/dialer.go 为已归档的 CONNECT 隧道方案，未接线。
package kcross

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/zxh326/kite/pkg/model"
)

const (
	// DefaultKubernetesCapability 约定的 K8s 能力名（kcross tunnel 默认声明）
	DefaultKubernetesCapability = "kubernetes"
	// DefaultPrometheusCapability 约定的 Prometheus 能力名
	DefaultPrometheusCapability = "prometheus"

	discoverCacheTTL    = 3 * time.Second
	adminRequestTimeout = 5 * time.Second
)

// Capability 单个能力的发现信息
type Capability struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Health   string `json:"health"`
	Endpoint string `json:"endpoint,omitempty"`
}

// Peer 发现到的节点；Gateway 为该节点归属网关的 URL
type Peer struct {
	ID           string       `json:"id"`
	Status       string       `json:"status"` // online | offline
	ConnectedAt  string       `json:"connectedAt,omitempty"`
	Capabilities []Capability `json:"capabilities"`
	Gateway      string       `json:"gateway"`
}

// Settings 集成配置快照；Gateways 为逗号分隔的网关 URL 列表
type Settings struct {
	Gateways []string
	Token    string
	Enabled  bool
}

// GetSettings 从 kcross 独立配置表读取集成配置
func GetSettings() (Settings, error) {
	setting, err := model.GetKcrossSetting()
	if err != nil {
		return Settings{}, fmt.Errorf("load kcross setting: %w", err)
	}
	gateways := parseGateways(setting.GatewayURL)
	token := strings.TrimSpace(string(setting.AdminToken))
	return Settings{
		Gateways: gateways,
		Token:    token,
		Enabled:  setting.Enabled && len(gateways) > 0 && token != "",
	}, nil
}

func parseGateways(raw string) []string {
	var out []string
	for _, g := range strings.Split(raw, ",") {
		if g = strings.TrimSpace(g); g != "" {
			out = append(out, strings.TrimRight(g, "/"))
		}
	}
	return out
}

// BuildZeroKubeconfig 生成零凭证 kubeconfig：server 指向网关 http 反代路径，
// 无任何用户凭证——身份由 peer 侧按网关断言注入的 SA 决定
func BuildZeroKubeconfig(gatewayURL, peerID, capability string) string {
	server := fmt.Sprintf("%s/proxy/%s/%s",
		strings.TrimRight(gatewayURL, "/"), peerID, capability)
	return fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- cluster:
    server: %s
  name: kcross
contexts:
- context:
    cluster: kcross
    user: kcross
  name: kcross
current-context: kcross
users:
- name: kcross
  user: {}
`, server)
}

// Discover 列出全部节点及能力明细（结果短暂缓存，供发现页轮询与在线判定共用）
func Discover(ctx context.Context) ([]Peer, error) {
	s, err := GetSettings()
	if err != nil {
		return nil, err
	}
	if !s.Enabled {
		return nil, fmt.Errorf("kcross integration is not configured")
	}
	return discoverCached(ctx, s)
}

// DiscoverWith 用指定配置发现（连通测试用，绕过缓存）
func DiscoverWith(ctx context.Context, s Settings) ([]Peer, error) {
	return fetchPeers(ctx, s)
}

// PeerOnline 节点当前是否在线；未配置/查询失败按离线处理（fail closed）
func PeerOnline(ctx context.Context, peerID string) bool {
	peers, err := Discover(ctx)
	if err != nil {
		return false
	}
	for _, p := range peers {
		if p.ID == peerID {
			return p.Status == "online"
		}
	}
	return false
}

var discoverState struct {
	sync.Mutex
	peers     []Peer
	expiresAt time.Time
}

// discoverCached 带缓存的发现；缓存过期才回源
func discoverCached(ctx context.Context, s Settings) ([]Peer, error) {
	discoverState.Lock()
	defer discoverState.Unlock()

	if len(discoverState.peers) > 0 && time.Now().Before(discoverState.expiresAt) {
		return discoverState.peers, nil
	}

	peers, err := fetchPeers(ctx, s)
	if err != nil {
		return nil, err
	}
	discoverState.peers = peers
	discoverState.expiresAt = time.Now().Add(discoverCacheTTL)
	return peers, nil
}

// fetchPeers 逐网关回源拉取节点与能力明细；单网关失败不阻断其余网关，
// 全部失败才报错
func fetchPeers(ctx context.Context, s Settings) ([]Peer, error) {
	peers := make([]Peer, 0)
	var firstErr error
	for _, gw := range s.Gateways {
		gwPeers, err := fetchPeersFromGateway(ctx, s, gw)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		peers = append(peers, gwPeers...)
	}
	if len(s.Gateways) > 0 && len(peers) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return peers, nil
}

func fetchPeersFromGateway(ctx context.Context, s Settings, gatewayURL string) ([]Peer, error) {
	var summaries []peerSummary
	if err := adminCall(ctx, s, gatewayURL, "ListPeers", map[string]any{}, &listPeersResult{Peers: &summaries}); err != nil {
		return nil, fmt.Errorf("kcross ListPeers: %w", err)
	}

	peers := make([]Peer, 0, len(summaries))
	for _, sum := range summaries {
		peer := Peer{
			ID:          sum.ID,
			Status:      "offline",
			ConnectedAt: sum.ConnectedAt,
			Gateway:     gatewayURL,
		}
		if sum.Status == peerStatusOnline {
			peer.Status = "online"
		}

		var detail peerDetail
		if err := adminCall(ctx, s, gatewayURL, "GetPeer", map[string]any{"peerId": sum.ID}, &getPeerResult{Peer: &detail}); err == nil {
			for _, c := range detail.Capabilities {
				peer.Capabilities = append(peer.Capabilities, Capability{
					Name:     c.Name,
					Kind:     c.ShortKind(),
					Health:   c.Health,
					Endpoint: c.Endpoint(),
				})
			}
		}
		peers = append(peers, peer)
	}
	return peers, nil
}

// adminCall 调用 connect 一元 RPC（JSON over HTTP）；kcross Gateway 兼容该形态
func adminCall(ctx context.Context, s Settings, gatewayURL, method string, req any, out any) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal %s request: %w", method, err)
	}

	callCtx, cancel := context.WithTimeout(ctx, adminRequestTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost,
		strings.TrimRight(gatewayURL, "/")+adminAPIPrefix+"kcross.v1.AdminService/"+method,
		strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("build %s request: %w", method, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Connect-Protocol-Version", "1")
	httpReq.Header.Set("Authorization", "Bearer "+s.Token)

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("call %s: %w", method, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read %s response: %w", method, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned %d: %s", method, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode %s response: %w", method, err)
	}
	return nil
}

const (
	adminAPIPrefix   = "/api/v1/admin/"
	peerStatusOnline = "PEER_STATUS_ONLINE"
)

type peerSummary struct {
	ID           string   `json:"id"`
	Status       string   `json:"status"`
	Capabilities []string `json:"capabilities"`
	ConnectedAt  string   `json:"connectedAt"`
}

type listPeersResult struct {
	Peers *[]peerSummary `json:"peers"`
}

type peerCapability struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Health   string `json:"health"`
	HTTPSpec *struct {
		Endpoint string `json:"endpoint"`
	} `json:"httpProxy"`
	TCPSpec *struct {
		Target string `json:"target"`
	} `json:"tcpProxy"`
}

// ShortKind proto 枚举名压缩为 http/tcp
func (c peerCapability) ShortKind() string {
	switch c.Kind {
	case "CAPABILITY_KIND_HTTP_PROXY":
		return "http"
	case "CAPABILITY_KIND_TCP_PROXY":
		return "tcp"
	}
	return strings.ToLower(strings.TrimPrefix(c.Kind, "CAPABILITY_KIND_"))
}

// Endpoint 能力上游端点（oneof：httpProxy.endpoint / tcpProxy.target）
func (c peerCapability) Endpoint() string {
	if c.HTTPSpec != nil {
		return c.HTTPSpec.Endpoint
	}
	if c.TCPSpec != nil {
		return c.TCPSpec.Target
	}
	return ""
}

type peerDetail struct {
	ID           string           `json:"id"`
	Status       string           `json:"status"`
	Capabilities []peerCapability `json:"capabilities"`
	ConnectedAt  string           `json:"connectedAt"`
}

type getPeerResult struct {
	Peer *peerDetail `json:"peer"`
}
