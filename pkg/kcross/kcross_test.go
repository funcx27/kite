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
	t.Cleanup(func() { ln.Close() })
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
	defer conn.Close()
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
	defer conn.Close()

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

func TestFingerprint_StableAndSensitive(t *testing.T) {
	base := Fingerprint("http://gw", "tok", "peer", "cap")
	if base != Fingerprint("http://gw", "tok", "peer", "cap") {
		t.Fatal("fingerprint not deterministic")
	}
	for _, changed := range []struct{ url, tok, peer, cap string }{
		{"http://gw2", "tok", "peer", "cap"},
		{"http://gw", "tok2", "peer", "cap"},
		{"http://gw", "tok", "peer2", "cap"},
		{"http://gw", "tok", "peer", "cap2"},
	} {
		if Fingerprint(changed.url, changed.tok, changed.peer, changed.cap) == base {
			t.Fatalf("fingerprint insensitive to change: %+v", changed)
		}
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
	if err := adminCall(context.Background(), Settings{GatewayURL: ts.URL, Token: "tok"}, "ListPeers", map[string]any{}, &out); err != nil {
		t.Fatalf("adminCall: %v", err)
	}
	if len(summaries) != 1 || summaries[0].ID != "p1" || summaries[0].Status != peerStatusOnline {
		t.Fatalf("parsed = %+v", summaries)
	}
}
