package kcross

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeGateway 模拟 kcross Gateway 的 CONNECT 端点：校验 Bearer 与路径，
// 200 后把隧道桥接到本地 echo
type fakeGateway struct {
	ln      net.Listener
	token   string
	gotPath chan string
	gotAuth chan string
}

func newFakeGateway(t *testing.T, token string) *fakeGateway {
	t.Helper()

	gw := &fakeGateway{
		token:   token,
		gotPath: make(chan string, 1),
		gotAuth: make(chan string, 1),
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	gw.ln = ln
	go gw.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return gw
}

func (g *fakeGateway) addr() string { return g.ln.Addr().String() }

func (g *fakeGateway) serve() {
	for {
		conn, err := g.ln.Accept()
		if err != nil {
			return
		}
		go g.handle(conn)
	}
}

func (g *fakeGateway) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	br := bufio.NewReader(conn)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	auth := req.Header.Get("Authorization")
	select {
	case g.gotPath <- req.URL.Path:
	default:
	}
	select {
	case g.gotAuth <- auth:
	default:
	}

	if req.Method != http.MethodConnect {
		_ = writeStatus(conn, http.StatusMethodNotAllowed)
		return
	}
	if g.token != "" && auth != "Bearer "+g.token {
		_ = writeStatus(conn, http.StatusUnauthorized)
		return
	}
	_ = writeStatus(conn, http.StatusOK)
	// CONNECT 后双向 echo
	_, _ = io.Copy(conn, conn)
}

func writeStatus(conn net.Conn, code int) error {
	_, err := fmt.Fprintf(conn, "HTTP/1.1 %d %s\r\nContent-Length: 0\r\n\r\n", code, http.StatusText(code))
	return err
}

func TestDialer_SuccessEchoThroughTunnel(t *testing.T) {
	gw := newFakeGateway(t, "secret")

	d := &Dialer{
		GatewayURL: "http://" + gw.addr(),
		Token:      "secret",
		PeerID:     "peer-1",
		Capability: "kubernetes",
	}
	conn, err := d.DialContext(context.Background(), "tcp", "ignored:443")
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 4)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("echo got %q", buf)
	}

	select {
	case p := <-gw.gotPath:
		if p != "/peer/peer-1/kubernetes" {
			t.Fatalf("CONNECT path = %q", p)
		}
	default:
		t.Fatal("gateway did not observe CONNECT")
	}
}

func TestDialer_Unauthorized(t *testing.T) {
	gw := newFakeGateway(t, "secret")

	d := &Dialer{
		GatewayURL: "http://" + gw.addr(),
		Token:      "wrong",
		PeerID:     "peer-1",
		Capability: "kubernetes",
	}
	_, err := d.DialContext(context.Background(), "tcp", "ignored:443")
	if err == nil {
		t.Fatal("expected error for unauthorized CONNECT")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("error should mention 401, got: %v", err)
	}
}

func TestDialer_RejectsNonTCP(t *testing.T) {
	d := &Dialer{GatewayURL: "http://127.0.0.1:1", PeerID: "p", Capability: "c"}
	if _, err := d.DialContext(context.Background(), "udp", "x:1"); err == nil {
		t.Fatal("expected error for udp network")
	}
}

func TestAdminCall_ParsesConnectJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("auth = %q", got)
		}
		if !strings.HasSuffix(r.URL.Path, "/api/v1/admin/kcross.v1.AdminService/ListPeers") {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"peers":[{"id":"p1","status":"PEER_STATUS_ONLINE","capabilities":["kubernetes"],"connectedAt":"2026-01-01T00:00:00Z"}]}`))
	}))
	defer ts.Close()

	var out listPeersResult
	summaries := []peerSummary{}
	out.Peers = &summaries
	if err := adminCall(context.Background(), Settings{Token: "tok"}, ts.URL, "ListPeers", map[string]any{}, &out); err != nil {
		t.Fatalf("adminCall: %v", err)
	}
	if len(summaries) != 1 || summaries[0].ID != "p1" || summaries[0].Status != peerStatusOnline {
		t.Fatalf("parsed = %+v", summaries)
	}
}

// TestDiscoverWith_MultiGateway 多网关聚合：单网关失败不阻断其余
func TestDiscoverWith_MultiGateway(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "ListPeers") {
			_, _ = w.Write([]byte(`{"peers":[{"id":"p-good","status":"PEER_STATUS_ONLINE","capabilities":["kubernetes"]}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"peer":{"id":"p-good","status":"PEER_STATUS_ONLINE","capabilities":[{"name":"kubernetes","kind":"CAPABILITY_KIND_HTTP_PROXY","health":"healthy","httpProxy":{"endpoint":"https://1.2.3.4:6443"}}]}}`))
	}))
	defer good.Close()
	dead := "http://127.0.0.1:1"

	s := Settings{Gateways: []string{dead, good.URL}, Token: "tok", Enabled: true}
	peers, err := DiscoverWith(context.Background(), s)
	if err != nil {
		t.Fatalf("DiscoverWith: %v", err)
	}
	if len(peers) != 1 || peers[0].ID != "p-good" {
		t.Fatalf("peers = %+v", peers)
	}
	if peers[0].Gateway != good.URL {
		t.Fatalf("peer gateway = %q, want %q", peers[0].Gateway, good.URL)
	}
	if len(peers[0].Capabilities) != 1 || peers[0].Capabilities[0].Endpoint != "https://1.2.3.4:6443" {
		t.Fatalf("capabilities = %+v", peers[0].Capabilities)
	}
}

func TestDiscoverWith_AllGatewaysFail(t *testing.T) {
	s := Settings{Gateways: []string{"http://127.0.0.1:1"}, Token: "tok", Enabled: true}
	if _, err := DiscoverWith(context.Background(), s); err == nil {
		t.Fatal("expected error when all gateways fail")
	}
}

func TestBuildZeroKubeconfig(t *testing.T) {
	yaml := BuildZeroKubeconfig("http://gw:18080", "peer-1", "kubernetes")
	for _, want := range []string{
		"server: http://gw:18080/proxy/peer-1/kubernetes",
		"apiVersion: v1",
		"user: {}",
	} {
		if !strings.Contains(yaml, want) {
			t.Fatalf("kubeconfig missing %q:\n%s", want, yaml)
		}
	}
	if strings.Contains(strings.Split(yaml, "users:")[1], "token") {
		t.Fatal("zero-credential kubeconfig must not embed tokens")
	}
}
