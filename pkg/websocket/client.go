package websocket

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Catker/acmeDeliver/pkg/cert"
	"github.com/Catker/acmeDeliver/pkg/security"
)

const (
	// 写入等待超时
	writeWait = 10 * time.Second

	// 读取下一个 pong 消息的等待时间
	pongWait = 120 * time.Second

	// 发送 ping 的周期，必须小于 pongWait
	pingPeriod = (pongWait * 9) / 10

	// 最大消息大小
	maxMessageSize = 10 * 1024 * 1024 // 10MB (证书文件可能较大)
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // 直接允许所有来源（已有 IP 白名单保护）
	},
}

// Client 表示一个 WebSocket 客户端连接
type Client struct {
	ID      string // 客户端标识
	hub     *Hub   // 所属的 Hub
	conn    *websocket.Conn
	send    chan *Message // 发送消息缓冲区
	domains []string      // 订阅的域名列表
	baseDir string        // 证书目录（用于响应 CLI 请求）

	// 状态查询字段
	RemoteIP    string    // 客户端 IP 地址
	ConnectedAt time.Time // 连接建立时间

	authenticated bool       // 是否已认证
	mu            sync.Mutex // 保护 conn 的并发写入
}

// NewClient 创建新的客户端连接
func NewClient(hub *Hub, conn *websocket.Conn) *Client {
	return &Client{
		hub:  hub,
		conn: conn,
		send: make(chan *Message, 256),
	}
}

// ServeWs 处理 WebSocket 升级请求
// trustProxy 控制是否信任 X-Forwarded-For/X-Real-IP 头部
func ServeWs(hub *Hub, password, baseDir string, whitelist *security.IPWhitelist, trustProxy bool, w http.ResponseWriter, r *http.Request) {
	// IP 白名单验证（在 WebSocket 升级之前）
	clientIP := extractClientIP(r, trustProxy)
	if !whitelist.IsAllowed(clientIP) {
		slog.Warn("IP 白名单拒绝连接", "ip", clientIP)
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("WebSocket 升级失败", "error", err)
		return
	}

	slog.Debug("WebSocket 连接已建立", "ip", clientIP)

	client := NewClient(hub, conn)
	client.baseDir = baseDir
	client.RemoteIP = clientIP
	client.ConnectedAt = time.Now()

	// 创建认证处理器
	authHandler := &AuthHandler{
		client:   client,
		verifier: security.NewSignatureVerifier(password),
		hub:      hub,
	}

	// 启动读写协程
	go client.writePump()
	go client.readPump(authHandler)
}

// extractClientIP 从请求中提取客户端真实 IP
// trustProxy 控制是否信任反向代理头部 (X-Forwarded-For, X-Real-IP)
// 安全注意：仅当服务部署在可信反向代理后时才应设置 trustProxy=true
// 否则攻击者可伪造这些头部绕过 IP 白名单
func extractClientIP(r *http.Request, trustProxy bool) string {
	// 始终先获取直连 IP（这是唯一可信的来源）
	remoteIP := extractRemoteAddr(r)

	// 仅当明确信任代理时才读取代理头
	if !trustProxy {
		return remoteIP
	}

	// 优先检查 X-Forwarded-For 头（反向代理）
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// X-Forwarded-For 可能包含多个 IP，取第一个
		if idx := strings.Index(xff, ","); idx != -1 {
			return strings.TrimSpace(xff[:idx])
		}
		return strings.TrimSpace(xff)
	}

	// 检查 X-Real-IP 头（Nginx 常用）
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}

	// 无代理头时返回直连 IP
	return remoteIP
}

// extractRemoteAddr 从 RemoteAddr 提取直连 IP
// 格式: ip:port 或 [ipv6]:port
func extractRemoteAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// 可能没有端口号
		return r.RemoteAddr
	}
	return host
}

// AuthHandler 处理客户端认证
type AuthHandler struct {
	client   *Client
	verifier *security.SignatureVerifier
	hub      *Hub
}

// HandleAuth 处理认证请求
func (h *AuthHandler) HandleAuth(msg *Message) bool {
	var req AuthRequest
	if err := msg.ParseData(&req); err != nil {
		h.sendAuthResult(false, "无效的认证数据")
		return false
	}

	// 使用统一的签名验证器
	ok, errMsg := h.verifier.VerifySignature(req.Signature, msg.Timestamp)
	if !ok {
		h.sendAuthResult(false, errMsg)
		return false
	}

	// 认证成功
	h.client.ID = req.ClientID
	h.client.domains = req.Domains
	h.client.authenticated = true

	// 注册到 Hub
	h.hub.Register(h.client)

	h.sendAuthResult(true, "认证成功")
	return true
}

func (h *AuthHandler) sendAuthResult(success bool, message string) {
	resp := &AuthResponse{
		Success: success,
		Message: message,
	}
	msg, _ := NewMessage(MsgTypeAuthResult, resp)
	h.client.sendMessage(msg)
}

// readPump 从 WebSocket 读取消息
func (c *Client) readPump(authHandler *AuthHandler) {
	defer func() {
		if c.authenticated {
			c.hub.Unregister(c)
		}
		c.conn.Close()
	}()

	c.conn.SetReadLimit(maxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				slog.Warn("WebSocket 读取错误", "client_id", c.ID, "error", err)
			}
			break
		}

		var msg Message
		if err := json.Unmarshal(data, &msg); err != nil {
			slog.Warn("无效的消息格式", "error", err)
			continue
		}

		c.handleMessage(&msg, authHandler)
	}
}

// handleMessage 处理收到的消息
func (c *Client) handleMessage(msg *Message, authHandler *AuthHandler) {
	switch msg.Type {
	case MsgTypeAuth:
		authHandler.HandleAuth(msg)

	case MsgTypePing:
		// 响应心跳
		pong, _ := NewMessage(MsgTypePong, nil)
		c.sendMessage(pong)

	case MsgTypeCertAck:
		// 处理证书接收确认
		var ack CertAck
		if err := msg.ParseData(&ack); err == nil {
			slog.Debug("收到证书确认",
				"client_id", c.ID,
				"domain", ack.Domain,
				"success", ack.Success)
		}

	case MsgTypeSubscribe:
		// 未认证客户端不允许发送订阅请求
		if !c.authenticated {
			errMsg, _ := NewMessage(MsgTypeError, &ErrorData{
				Code:    401,
				Message: "请先进行认证",
			})
			c.sendMessage(errMsg)
			return
		}
		// 处理订阅更新
		var req SubscribeRequest
		if err := msg.ParseData(&req); err != nil {
			slog.Warn("无效的订阅请求数据", "client_id", c.ID, "error", err)
			return
		}
		c.hub.UpdateSubscription(c, req.Domains)
		slog.Debug("客户端订阅更新请求已处理", "client_id", c.ID, "domains", req.Domains)

	case MsgTypeCertRequest:
		// 处理证书请求（CLI 模式）
		if !c.authenticated {
			c.sendAuthError()
			return
		}
		c.handleCertRequest(msg)

	case MsgTypeStatusRequest:
		// 处理状态请求（CLI 模式）
		if !c.authenticated {
			c.sendAuthError()
			return
		}
		c.handleStatusRequest(msg)

	case MsgTypeSyncRequest:
		// 处理证书同步请求（Daemon 模式）
		if !c.authenticated {
			c.sendAuthError()
			return
		}
		c.handleSyncRequest(msg)

	default:
		if !c.authenticated {
			// 未认证的客户端只能发送认证请求
			c.sendAuthError()
		}
	}
}

// writePump 向 WebSocket 写入消息
func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case msg, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// Hub 关闭了通道
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			data, err := json.Marshal(msg)
			if err != nil {
				slog.Error("消息序列化失败", "error", err)
				continue
			}

			c.mu.Lock()
			err = c.conn.WriteMessage(websocket.TextMessage, data)
			c.mu.Unlock()

			if err != nil {
				slog.Warn("WebSocket 写入失败", "client_id", c.ID, "error", err)
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			c.mu.Lock()
			err := c.conn.WriteMessage(websocket.PingMessage, nil)
			c.mu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

// sendMessage 发送消息到客户端
func (c *Client) sendMessage(msg *Message) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.conn.SetWriteDeadline(time.Now().Add(writeWait))
	c.conn.WriteMessage(websocket.TextMessage, data)
}

// sendAuthError 发送认证错误响应
func (c *Client) sendAuthError() {
	errMsg, _ := NewMessage(MsgTypeError, &ErrorData{
		Code:    401,
		Message: "请先进行认证",
	})
	c.sendMessage(errMsg)
}

// handleCertRequest 处理证书请求（CLI 模式）
func (c *Client) handleCertRequest(msg *Message) {
	var req CertRequest
	if err := msg.ParseData(&req); err != nil {
		c.sendCertResponse(req.Domain, nil, 0, "无效的请求数据")
		return
	}

	if req.Domain == "" {
		c.sendCertResponse("", nil, 0, "域名不能为空")
		return
	}

	slog.Debug("处理证书请求", "client_id", c.ID, "domain", req.Domain, "force", req.Force)

	// 读取证书文件
	domainDir := filepath.Join(c.baseDir, req.Domain)
	if _, err := os.Stat(domainDir); os.IsNotExist(err) {
		c.sendCertResponse(req.Domain, nil, 0, "域名不存在")
		return
	}

	// 读取所有证书文件
	files := make(map[string][]byte)
	certFiles := []string{"cert.pem", "key.pem", "fullchain.pem", "time.log"}

	for _, filename := range certFiles {
		filePath := filepath.Join(domainDir, filename)
		content, err := os.ReadFile(filePath)
		if err == nil {
			files[filename] = content
		}
	}

	if len(files) == 0 {
		c.sendCertResponse(req.Domain, nil, 0, "没有可用的证书文件")
		return
	}

	// 获取时间戳
	var timestamp int64
	if timeContent, ok := files["time.log"]; ok {
		// 解析时间戳
		ts := string(timeContent)
		ts = ts[:min(len(ts), 10)] // 只取前10位
		if t, err := strconv.ParseInt(ts, 10, 64); err == nil {
			timestamp = t
		}
	}

	c.sendCertResponse(req.Domain, files, timestamp, "")
	slog.Info("证书请求已处理", "client_id", c.ID, "domain", req.Domain, "files", len(files))
}

// sendCertResponse 发送证书响应
func (c *Client) sendCertResponse(domain string, files map[string][]byte, timestamp int64, errMsg string) {
	resp := &CertResponse{
		Domain:    domain,
		Files:     files,
		Timestamp: timestamp,
		Error:     errMsg,
	}
	msg, _ := NewMessage(MsgTypeCertResponse, resp)
	c.sendMessage(msg)
}

// handleStatusRequest 处理状态请求（CLI 模式）
// 返回服务器运行状态：在线客户端 + 证书状态
func (c *Client) handleStatusRequest(msg *Message) {
	slog.Debug("处理状态请求", "client_id", c.ID)

	// 收集客户端状态
	clientStatus := c.hub.GetClientStatus()
	clients := make([]ClientStatusInfo, 0, len(clientStatus))
	for _, cs := range clientStatus {
		clients = append(clients, ClientStatusInfo{
			ID:          cs.ID,
			RemoteIP:    cs.RemoteIP,
			ConnectedAt: cs.ConnectedAt.Unix(),
			Domains:     cs.Domains,
		})
	}

	// 收集证书状态
	domains := cert.CollectAllDomainStatus(c.baseDir)

	c.sendStatusResponse(clients, domains, "")
	slog.Info("状态请求已处理", "client_id", c.ID, "clients", len(clients), "domains", len(domains))
}

// sendStatusResponse 发送状态响应
func (c *Client) sendStatusResponse(clients []ClientStatusInfo, domains []DomainStatus, errMsg string) {
	resp := &StatusResponse{
		GeneratedAt: time.Now().Unix(),
		Clients:     clients,
		Domains:     domains,
		Error:       errMsg,
	}
	msg, _ := NewMessage(MsgTypeStatusResponse, resp)
	c.sendMessage(msg)
}

// handleSyncRequest 处理证书同步请求（Daemon 模式）
// 比对客户端提交的时间戳，推送需要更新的证书
func (c *Client) handleSyncRequest(msg *Message) {
	var req SyncRequest
	if err := msg.ParseData(&req); err != nil {
		slog.Warn("无效的同步请求数据", "client_id", c.ID, "error", err)
		return
	}

	slog.Info("处理证书同步请求", "client_id", c.ID, "domains", len(req.Timestamps))

	pushedCount := 0

	// 遍历客户端订阅的域名
	for _, domain := range c.domains {
		// 全局订阅 "*" 需要特殊处理：推送所有本地有但客户端未提供时间戳的域名
		if domain == "*" {
			pushedCount += c.syncAllDomains(req.Timestamps)
			continue
		}

		// 获取客户端的本地时间戳
		clientTS := req.Timestamps[domain]

		// 读取服务端时间戳
		serverTS := c.readServerTimestamp(domain)
		if serverTS == 0 {
			// 服务端无此域名证书，跳过
			continue
		}

		// 比对时间戳：服务端更新时才推送
		if serverTS > clientTS {
			if c.pushCertToDomain(domain) {
				pushedCount++
			}
		}
	}

	slog.Info("证书同步请求处理完成", "client_id", c.ID, "pushed", pushedCount)
}

// syncAllDomains 同步所有域名（用于全局订阅 "*"）
func (c *Client) syncAllDomains(clientTimestamps map[string]int64) int {
	entries, err := os.ReadDir(c.baseDir)
	if err != nil {
		slog.Warn("读取证书目录失败", "error", err)
		return 0
	}

	pushedCount := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		domain := entry.Name()

		// 读取服务端时间戳
		serverTS := c.readServerTimestamp(domain)
		if serverTS == 0 {
			continue
		}

		// 获取客户端时间戳（不存在则为 0）
		clientTS := clientTimestamps[domain]

		// 比对时间戳
		if serverTS > clientTS {
			if c.pushCertToDomain(domain) {
				pushedCount++
			}
		}
	}

	return pushedCount
}

// readServerTimestamp 读取服务端指定域名的时间戳
func (c *Client) readServerTimestamp(domain string) int64 {
	timeLogPath := filepath.Join(c.baseDir, domain, "time.log")
	content, err := os.ReadFile(timeLogPath)
	if err != nil {
		return 0
	}

	ts := strings.TrimSpace(string(content))
	if len(ts) > 10 {
		ts = ts[:10]
	}

	if t, err := strconv.ParseInt(ts, 10, 64); err == nil {
		return t
	}
	return 0
}

// pushCertToDomain 推送指定域名的证书给当前客户端
func (c *Client) pushCertToDomain(domain string) bool {
	domainDir := filepath.Join(c.baseDir, domain)

	// 读取证书文件
	files := make(map[string][]byte)
	certFiles := []string{"cert.pem", "key.pem", "fullchain.pem", "time.log"}

	for _, filename := range certFiles {
		filePath := filepath.Join(domainDir, filename)
		content, err := os.ReadFile(filePath)
		if err == nil {
			files[filename] = content
		}
	}

	if len(files) == 0 {
		return false
	}

	// 获取时间戳
	var timestamp int64
	if timeContent, ok := files["time.log"]; ok {
		ts := strings.TrimSpace(string(timeContent))
		if len(ts) > 10 {
			ts = ts[:10]
		}
		if t, err := strconv.ParseInt(ts, 10, 64); err == nil {
			timestamp = t
		}
	}

	// 构建推送消息
	data := &CertPushData{
		Domain:    domain,
		Files:     files,
		Timestamp: timestamp,
	}

	msg, err := NewMessage(MsgTypeCertPush, data)
	if err != nil {
		return false
	}

	// 发送消息
	select {
	case c.send <- msg:
		slog.Debug("同步推送证书", "client_id", c.ID, "domain", domain)
		return true
	default:
		slog.Warn("同步推送证书失败：发送缓冲区已满", "client_id", c.ID, "domain", domain)
		return false
	}
}
