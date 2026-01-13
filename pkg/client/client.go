// Package client 提供客户端功能
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Catker/acmeDeliver/pkg/security"
	ws "github.com/Catker/acmeDeliver/pkg/websocket"
)

// WSClient WebSocket 统一客户端
// 提供 CLI 一次性操作（下载证书、列表查询）和 Daemon 模式共用的底层通信
type WSClient struct {
	serverURL string
	password  string
	tlsConfig *TLSConfig // TLS 配置（可选）
	conn      *websocket.Conn
	mu        sync.Mutex

	// 响应等待
	responses     map[string]chan *ws.Message
	responsesMu   sync.Mutex
	authenticated bool
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
	// 解析服务器地址
	wsURL := c.serverURL
	if !strings.HasPrefix(wsURL, "ws://") && !strings.HasPrefix(wsURL, "wss://") {
		wsURL = strings.Replace(wsURL, "http://", "ws://", 1)
		wsURL = strings.Replace(wsURL, "https://", "wss://", 1)
	}
	// 确保所有 URL 都追加 /ws 后缀
	if !strings.HasSuffix(wsURL, "/ws") {
		wsURL = wsURL + "/ws"
	}

	slog.Debug("正在连接服务器", "url", wsURL)

	// 构建 TLS 配置
	tlsConfig, err := BuildTLSConfig(c.tlsConfig)
	if err != nil {
		return fmt.Errorf("TLS 配置错误: %w", err)
	}

	// 建立连接（带连接超时）
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		TLSClientConfig:  tlsConfig,
	}
	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("连接服务器失败: %w", err)
	}
	c.conn = conn

	// 启动消息读取循环
	go c.readLoop()

	// 发送认证请求
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

	// 使用统一的签名验证器生成签名
	verifier := security.NewSignatureVerifier(c.password)
	signature := verifier.GenerateSignature(timestamp)

	authReq := &ws.AuthRequest{
		ClientID:  "cli-client",
		Signature: signature,
		Domains:   []string{}, // CLI 模式不订阅任何域名
	}

	msg, err := ws.NewMessage(ws.MsgTypeAuth, authReq)
	if err != nil {
		return err
	}
	msg.Timestamp = timestamp

	// 注册响应等待
	respChan := c.registerResponse(ws.MsgTypeAuthResult)
	defer c.unregisterResponse(ws.MsgTypeAuthResult)

	// 发送认证请求
	if err := c.sendMessage(msg); err != nil {
		return err
	}

	// 等待认证响应
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(10 * time.Second):
		return fmt.Errorf("认证超时")
	case resp := <-respChan:
		var authResp ws.AuthResponse
		if err := resp.ParseData(&authResp); err != nil {
			return fmt.Errorf("解析认证响应失败: %w", err)
		}
		if !authResp.Success {
			return fmt.Errorf("认证被拒绝: %s", authResp.Message)
		}
		c.authenticated = true
		return nil
	}
}

// DownloadCert 下载证书（CLI 一次性操作）
func (c *WSClient) DownloadCert(ctx context.Context, domain string, force bool) (*CertificateFiles, error) {
	if !c.authenticated {
		return nil, fmt.Errorf("未认证")
	}

	req := &ws.CertRequest{
		Domain: domain,
		Force:  force,
	}

	msg, err := ws.NewMessage(ws.MsgTypeCertRequest, req)
	if err != nil {
		return nil, err
	}

	// 注册响应等待
	respChan := c.registerResponse(ws.MsgTypeCertResponse)
	defer c.unregisterResponse(ws.MsgTypeCertResponse)

	// 发送请求
	if err := c.sendMessage(msg); err != nil {
		return nil, err
	}

	// 等待响应
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(30 * time.Second):
		return nil, fmt.Errorf("请求超时")
	case resp := <-respChan:
		var certResp ws.CertResponse
		if err := resp.ParseData(&certResp); err != nil {
			return nil, fmt.Errorf("解析响应失败: %w", err)
		}
		if certResp.Error != "" {
			return nil, fmt.Errorf("服务器错误: %s", certResp.Error)
		}

		// 转换为 CertificateFiles
		certs := &CertificateFiles{}
		if data, ok := certResp.Files["cert.pem"]; ok {
			certs.Cert = data
		}
		if data, ok := certResp.Files["key.pem"]; ok {
			certs.Key = data
		}
		if data, ok := certResp.Files["fullchain.pem"]; ok {
			certs.Fullchain = data
		}
		return certs, nil
	}
}

// GetServerStatus 获取服务器状态（在线客户端 + 证书状态）
func (c *WSClient) GetServerStatus(ctx context.Context) (*ws.StatusResponse, error) {
	if !c.authenticated {
		return nil, fmt.Errorf("未认证")
	}

	req := &ws.StatusRequest{}

	msg, err := ws.NewMessage(ws.MsgTypeStatusRequest, req)
	if err != nil {
		return nil, err
	}

	// 注册响应等待
	respChan := c.registerResponse(ws.MsgTypeStatusResponse)
	defer c.unregisterResponse(ws.MsgTypeStatusResponse)

	// 发送请求
	if err := c.sendMessage(msg); err != nil {
		return nil, err
	}

	// 等待响应
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(10 * time.Second):
		return nil, fmt.Errorf("请求超时")
	case resp := <-respChan:
		var statusResp ws.StatusResponse
		if err := resp.ParseData(&statusResp); err != nil {
			return nil, fmt.Errorf("解析响应失败: %w", err)
		}
		if statusResp.Error != "" {
			return nil, fmt.Errorf("服务器错误: %s", statusResp.Error)
		}
		return &statusResp, nil
	}
}

// sendMessage 发送消息
func (c *WSClient) sendMessage(msg *ws.Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	return c.conn.WriteMessage(websocket.TextMessage, data)
}

// readLoop 消息读取循环
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

		// 分发消息到等待的响应通道
		c.dispatchResponse(&msg)
	}
}

// registerResponse 注册响应等待通道
func (c *WSClient) registerResponse(msgType string) chan *ws.Message {
	c.responsesMu.Lock()
	defer c.responsesMu.Unlock()

	ch := make(chan *ws.Message, 1)
	c.responses[msgType] = ch
	return ch
}

// unregisterResponse 注销响应等待通道
func (c *WSClient) unregisterResponse(msgType string) {
	c.responsesMu.Lock()
	defer c.responsesMu.Unlock()

	delete(c.responses, msgType)
}

// dispatchResponse 分发响应到等待通道
func (c *WSClient) dispatchResponse(msg *ws.Message) {
	c.responsesMu.Lock()
	ch, ok := c.responses[msg.Type]
	c.responsesMu.Unlock()

	if ok {
		select {
		case ch <- msg:
		default:
			slog.Warn("响应通道已满，丢弃消息", "type", msg.Type)
		}
	}
}
