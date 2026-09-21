package kcross

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"
)

// Dialer 经 kcross Gateway 的 CONNECT 隧道拨号到指定 Peer 能力。
// 目标固定为能力声明的上游，addr 参数被忽略（client-go 的拨号地址是
// kubeconfig 里的 apiserver，真实连接由 Peer 侧按能力路由）
type Dialer struct {
	GatewayURL  string // http(s)://host:port
	Token       string // Gateway Bearer token；空 = 网关未启用鉴权
	PeerID      string
	Capability  string
	DialTimeout time.Duration // 建立连接（含 CONNECT 握手）超时；0 = 10s
}

// DialContext 实现 rest.Config.Dial：向 Gateway 发 path-form CONNECT，
// 200 后返回承载连接，上层 TLS（kubeconfig 的 CA/SNI）端到端直达上游
func (d *Dialer) DialContext(ctx context.Context, network, _ string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, fmt.Errorf("kcross dialer: unsupported network %q", network)
	}

	timeout := d.DialTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	gw, err := d.dialGateway(ctx, timeout)
	if err != nil {
		return nil, fmt.Errorf("kcross dial gateway: %w", err)
	}
	defer func() {
		if err != nil {
			gw.Close()
		}
	}()

	connectCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	_ = gw.SetDeadline(deadline(connectCtx))
	if err = writeConnect(gw, d.PeerID, d.Capability, d.Token); err != nil {
		return nil, fmt.Errorf("kcross write CONNECT: %w", err)
	}
	conn, err := readConnectResponse(gw, d.PeerID, d.Capability)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

// dialGateway 建立 TCP 连接；https 网关补一层 TLS（SNI 取网关主机名）
func (d *Dialer) dialGateway(ctx context.Context, timeout time.Duration) (net.Conn, error) {
	u, err := url.Parse(d.GatewayURL)
	if err != nil {
		return nil, fmt.Errorf("parse gateway url: %w", err)
	}
	host := u.Host
	if u.Port() == "" {
		if u.Scheme == "https" {
			host = net.JoinHostPort(u.Hostname(), "443")
		} else {
			host = net.JoinHostPort(u.Hostname(), "80")
		}
	}

	var dialer net.Dialer
	raw, err := dialer.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "https" {
		return raw, nil
	}

	tlsConn := tls.Client(raw, &tls.Config{
		ServerName:         u.Hostname(),
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: false,
	})
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		raw.Close()
		return nil, fmt.Errorf("gateway tls handshake: %w", err)
	}
	return tlsConn, nil
}

// writeConnect 手写 path-form CONNECT 请求行（带可选 Bearer）
func writeConnect(conn net.Conn, peerID, capability, token string) error {
	if _, err := fmt.Fprintf(conn, "CONNECT /peer/%s/%s HTTP/1.1\r\nHost: kcross\r\n", peerID, capability); err != nil {
		return err
	}
	if token != "" {
		if _, err := fmt.Fprintf(conn, "Authorization: Bearer %s\r\n", token); err != nil {
			return err
		}
	}
	_, err := fmt.Fprint(conn, "\r\n")
	return err
}

// readConnectResponse 读取 CONNECT 应答；200 时把 bufio 中可能已读入的
// 隧道数据挂回连接避免丢字节。连接关闭由调用方负责
func readConnectResponse(conn net.Conn, peerID, capability string) (net.Conn, error) {
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		return nil, fmt.Errorf("kcross read CONNECT response: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kcross CONNECT %s/%s got %s", peerID, capability, resp.Status)
	}
	if br.Buffered() == 0 {
		return conn, nil
	}
	return &bufferedConn{Conn: conn, r: br}, nil
}

func deadline(ctx context.Context) time.Time {
	if t, ok := ctx.Deadline(); ok {
		return t
	}
	return time.Now().Add(10 * time.Second)
}

// bufferedConn 保留 CONNECT 应答后 bufio 里已缓冲的隧道数据
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }
