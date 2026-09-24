// Package client 提供客户端功能：CLI 一次性操作与 Daemon 模式
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Catker/acmeDeliver/pkg/security"
	ws "github.com/Catker/acmeDeliver/pkg/websocket"
)

// dial 规范化服务器地址（http(s):// 转 ws(s)://，追加 /ws）并建立 WebSocket 连接
func dial(ctx context.Context, serverURL string, tlsCfg *TLSConfig) (*websocket.Conn, error) {
	if !strings.HasPrefix(serverURL, "ws://") && !strings.HasPrefix(serverURL, "wss://") {
		serverURL = strings.Replace(serverURL, "http://", "ws://", 1)
		serverURL = strings.Replace(serverURL, "https://", "wss://", 1)
	}
	if !strings.HasSuffix(serverURL, "/ws") {
		serverURL += "/ws"
	}
	slog.Info("正在连接服务器", "url", serverURL)
	if isPlaintextRemote(serverURL) {
		slog.Warn("⚠️ 正在通过 ws:// 明文连接非本机服务器：证书私钥将明文传输，认证签名可在 30 秒内被重放；生产环境请使用 wss://，或仅在本机/内网反向代理后使用 ws://",
			"url", serverURL)
	}

	tlsConfig, err := BuildTLSConfig(tlsCfg)
	if err != nil {
		return nil, fmt.Errorf("TLS 配置错误: %w", err)
	}
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		TLSClientConfig:  tlsConfig,
	}
	conn, _, err := dialer.DialContext(ctx, serverURL, nil)
	return conn, err
}

// isPlaintextRemote 判断是否为 ws:// 明文连接且主机不是本机回环地址（localhost、127.0.0.0/8、::1）
func isPlaintextRemote(serverURL string) bool {
	u, err := url.Parse(serverURL)
	if err != nil || u.Scheme != "ws" {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return false
	}
	return true
}

// WSClient CLI 一次性操作（下载证书、状态查询）使用的 WebSocket 客户端
type WSClient struct {
	serverURL string
	password  string
	tlsConfig *TLSConfig // TLS 配置（可选）
	conn      *websocket.Conn

	// 响应等待：消息类型 -> 等待通道
	responses   map[string]chan *ws.Message
	responsesMu sync.Mutex
}

// NewWSClient 创建新的 WebSocket 客户端
// tlsConfig 可为 nil，表示使用系统默认 TLS 配置
func NewWSClient(serverURL, password string, tlsConfig *TLSConfig) *WSClient {
	return &WSClient{
		serverURL: serverURL,
		password:  password,
		tlsConfig: tlsConfig,
		responses: make(map[string]chan *ws.Message),
	}
}

// Connect 连接服务器并完成认证
func (c *WSClient) Connect(ctx context.Context) error {
	conn, err := dial(ctx, c.serverURL, c.tlsConfig)
	if err != nil {
		return fmt.Errorf("连接服务器失败: %w", err)
	}
	c.conn = conn

	go c.readLoop()

	if err := c.authenticate(ctx); err != nil {
		c.conn.Close()
		return fmt.Errorf("认证失败: %w", err)
	}

	slog.Debug("已连接并认证成功")
	return nil
}

// Close 关闭连接
func (c *WSClient) Close() {
	if c.conn != nil {
		c.conn.Close()
	}
}

// authenticate 发送认证请求并等待响应
func (c *WSClient) authenticate(ctx context.Context) error {
	timestamp := time.Now().Unix()
	msg, err := ws.NewMessage(ws.MsgTypeAuth, &ws.AuthRequest{
		ClientID:  "cli-client",
		Signature: security.NewSignatureVerifier(c.password).GenerateSignature(timestamp),
		Domains:   []string{}, // CLI 模式不订阅任何域名
	})
	if err != nil {
		return err
	}
	msg.Timestamp = timestamp

	var resp ws.AuthResponse
	if err := c.roundTrip(ctx, msg, ws.MsgTypeAuthResult, 10*time.Second, &resp); err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("认证被拒绝: %s", resp.Message)
	}
	return nil
}

// DownloadCert 下载证书，返回文件名 -> 内容与服务端时间戳（0 表示未知）
func (c *WSClient) DownloadCert(ctx context.Context, domain string) (map[string][]byte, int64, error) {
	msg, err := ws.NewMessage(ws.MsgTypeCertRequest, &ws.CertRequest{Domain: domain})
	if err != nil {
		return nil, 0, err
	}

	var resp ws.CertResponse
	if err := c.roundTrip(ctx, msg, ws.MsgTypeCertResponse, 30*time.Second, &resp); err != nil {
		return nil, 0, err
	}
	if resp.Error != "" {
		return nil, 0, fmt.Errorf("服务器错误: %s", resp.Error)
	}
	return resp.Files, resp.Timestamp, nil
}

// GetServerStatus 获取服务器状态（在线客户端 + 证书状态）
func (c *WSClient) GetServerStatus(ctx context.Context) (*ws.StatusResponse, error) {
	msg, err := ws.NewMessage(ws.MsgTypeStatusRequest, &ws.StatusRequest{})
	if err != nil {
		return nil, err
	}

	var resp ws.StatusResponse
	if err := c.roundTrip(ctx, msg, ws.MsgTypeStatusResponse, 10*time.Second, &resp); err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("服务器错误: %s", resp.Error)
	}
	return &resp, nil
}

// roundTrip 注册 respType 的响应等待、发送 msg，并在 timeout 内等待响应解析到 out
// CLI 顺序调用，同一时刻只有一个请求在途，conn 无并发写
func (c *WSClient) roundTrip(ctx context.Context, msg *ws.Message, respType string, timeout time.Duration, out interface{}) error {
	respChan := make(chan *ws.Message, 1)
	c.responsesMu.Lock()
	c.responses[respType] = respChan
	c.responsesMu.Unlock()
	defer func() {
		c.responsesMu.Lock()
		delete(c.responses, respType)
		c.responsesMu.Unlock()
	}()

	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(timeout):
		return fmt.Errorf("请求超时")
	case resp := <-respChan:
		if err := resp.ParseData(out); err != nil {
			return fmt.Errorf("解析响应失败: %w", err)
		}
		return nil
	}
}

// readLoop 读取消息并分发到等待对应类型响应的通道
func (c *WSClient) readLoop() {
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			slog.Debug("WebSocket 读取结束", "error", err)
			return
		}

		var msg ws.Message
		if err := json.Unmarshal(data, &msg); err != nil {
			slog.Warn("无效的消息格式", "error", err)
			continue
		}

		c.responsesMu.Lock()
		ch, ok := c.responses[msg.Type]
		c.responsesMu.Unlock()
		if ok {
			select {
			case ch <- &msg:
			default:
				slog.Warn("响应通道已满，丢弃消息", "type", msg.Type)
			}
		}
	}
}
