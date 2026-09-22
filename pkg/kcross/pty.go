package kcross

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// ptyFrameChannel WebSocket 二进制帧首字节即 pb.Channel 枚举：
// STDIN=0 STDOUT=1 STDERR=2 RESIZE=3 EXIT=4 OPEN=5 CLOSE=6 ERROR=7
const (
	ptyChannelResize = 3
	ptyChannelExit   = 4
	ptyChannelError  = 7
)

// HandlePeerPTY 代理节点 PTY 终端 WebSocket。
// 帧 [1字节 channel][payload] 端到端原样转发，kite 不解析内容；
// 鉴权分两段：浏览器→kite 走 admin 组 cookie 会话，kite→gateway 走 admin token
func HandlePeerPTY(c *gin.Context) {
	s, err := GetSettings()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	gateway := c.Query("gateway")
	if !isConfiguredGateway(s, gateway) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "gateway is not configured"})
		return
	}

	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return r.Header.Get("Origin") == "" || r.Header.Get("Origin") == "http://"+r.Host || r.Header.Get("Origin") == "https://"+r.Host
		},
	}
	clientConn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer clientConn.Close()

	gatewayConn, err := dialGatewayPTY(s.Token, gateway, c.Param("peerId"), c.Request.URL.Query())
	if err != nil {
		_ = clientConn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(1011, fmt.Sprintf("gateway dial failed: %v", err)),
			time.Now().Add(time.Second))
		return
	}
	defer gatewayConn.Close()

	done := make(chan struct{}, 2)
	go pipePTY(clientConn, gatewayConn, done)
	go pipePTY(gatewayConn, clientConn, done)
	<-done
}

// isConfiguredGateway 网关必须是集成配置里的已知地址，防代理滥用
func isConfiguredGateway(s Settings, gateway string) bool {
	for _, g := range s.Gateways {
		if g == strings.TrimRight(gateway, "/") {
			return true
		}
	}
	return false
}

// dialGatewayPTY 连接网关 PTY WebSocket，携带 cols/rows 供网关发起 OPEN
func dialGatewayPTY(token, gateway, peerID string, query url.Values) (*websocket.Conn, error) {
	gwURL, err := url.Parse(gateway)
	if err != nil {
		return nil, fmt.Errorf("parse gateway url: %w", err)
	}
	wsScheme := "ws"
	if gwURL.Scheme == "https" {
		wsScheme = "wss"
	}
	wsURL := url.URL{
		Scheme:   wsScheme,
		Host:     gwURL.Host,
		Path:     fmt.Sprintf("/api/v1/admin/peers/%s/pty", peerID),
		RawQuery: query.Encode(),
	}
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.Dial(wsURL.String(), http.Header{
		"Authorization": []string{"Bearer " + token},
	})
	if err != nil {
		return nil, err
	}
	return conn, nil
}

// pipePTY 单向转发；网关侧与浏览器侧帧格式一致，均为 [channel][payload]。
// 任一方向断开即关闭两条连接，另一方向的读随即报错退出
func pipePTY(dst, src *websocket.Conn, done chan<- struct{}) {
	defer func() { done <- struct{}{} }()
	for {
		msgType, data, err := src.ReadMessage()
		if err != nil {
			_ = dst.Close()
			_ = src.Close()
			return
		}
		if msgType != websocket.BinaryMessage || len(data) < 1 {
			continue
		}
		if err := dst.WriteMessage(websocket.BinaryMessage, data); err != nil {
			_ = dst.Close()
			_ = src.Close()
			return
		}
	}
}
