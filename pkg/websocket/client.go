package websocket

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Catker/acmeDeliver/pkg/cert"
	"github.com/Catker/acmeDeliver/pkg/security"
)

const (
	// 写入等待超时
	writeWait = 10 * time.Second

	// 读取下一个 pong 消息的等待时间
	pongWait = 60 * time.Second

	// 发送 ping 的周期，必须小于 pongWait；
	// 同时小于 nginx 默认 proxy_read_timeout(60s)，避免反向代理把空闲连接断开
	pingPeriod = 45 * time.Second

	// 最大消息大小
	maxMessageSize = 10 * 1024 * 1024 // 10MB (证书文件可能较大)
)

// authWait 连接建立后完成认证的时限，超时断开（变量仅为测试缩短）
var authWait = 10 * time.Second

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
	send    chan []byte // 已序列化消息的发送缓冲区（writePump 是 conn 的唯一写者）
	domains []string    // 订阅的域名列表
	baseDir string      // 证书目录（用于响应状态请求）
	certs   *cert.Store // 证书目录只读访问（用于响应 CLI 证书请求与同步请求）

	// 状态查询字段
	RemoteIP    string    // 客户端 IP 地址
	ConnectedAt time.Time // 连接建立时间

	verifier      *security.SignatureVerifier
	authenticated bool // 是否已认证（仅 readPump 协程读写）
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

	client := &Client{
		hub:         hub,
		conn:        conn,
		send:        make(chan []byte, 256),
		baseDir:     baseDir,
		certs:       cert.NewStore(baseDir),
		RemoteIP:    clientIP,
		ConnectedAt: time.Now(),
		verifier:    security.NewSignatureVerifier(password),
	}

	// 启动读写协程
	go client.writePump()
	go client.readPump()
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
	// 取最右一项：它由最近一跳（可信）代理追加，左侧各项可被客户端伪造
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if ip := strings.TrimSpace(xff[strings.LastIndex(xff, ",")+1:]); ip != "" {
			return ip
		}
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

// handleAuth 处理认证请求，返回 false 表示认证失败（调用方应关闭连接）
func (c *Client) handleAuth(msg *Message) bool {
	// 已认证连接忽略重复 auth：重复 Register 会让旧订阅残留在 hub 中
	if c.authenticated {
		slog.Warn("忽略已认证连接的重复认证请求", "client_id", c.ID)
		errMsg, _ := NewMessage(MsgTypeError, &ErrorData{
			Code:    400,
			Message: "已认证，忽略重复认证请求",
		})
		c.sendMessage(errMsg)
		return true
	}

	var req AuthRequest
	if err := msg.ParseData(&req); err != nil {
		c.sendAuthResult(false, "无效的认证数据")
		return false
	}

	ok, errMsg := c.verifier.VerifySignature(req.Signature, msg.Timestamp)
	if !ok {
		c.sendAuthResult(false, errMsg)
		return false
	}

	// 认证成功，注册到 Hub
	c.ID = req.ClientID
	c.domains = req.Domains
	c.authenticated = true
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.hub.Register(c)

	c.sendAuthResult(true, "认证成功")
	return true
}

func (c *Client) sendAuthResult(success bool, message string) {
	msg, _ := NewMessage(MsgTypeAuthResult, &AuthResponse{Success: success, Message: message})
	c.sendMessage(msg)
}

// readPump 从 WebSocket 读取消息
// 退出时关闭 send（已认证由 Hub 注销时关闭），writePump 写完剩余消息后关闭连接，
// 保证认证失败结果等已入队消息先写出
func (c *Client) readPump() {
	defer func() {
		if c.authenticated {
			c.hub.Unregister(c)
		} else {
			close(c.send)
		}
	}()

	c.conn.SetReadLimit(maxMessageSize)
	// 未认证连接只有 authWait 时间完成认证，pong 不续期；认证成功后改为 pongWait 保活
	c.conn.SetReadDeadline(time.Now().Add(authWait))
	c.conn.SetPongHandler(func(string) error {
		if c.authenticated {
			c.conn.SetReadDeadline(time.Now().Add(pongWait))
		}
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

		if !c.handleMessage(&msg) {
			break
		}
	}
}

// handleMessage 处理收到的消息，返回 false 表示应断开连接（认证失败）
func (c *Client) handleMessage(msg *Message) bool {
	// 未认证的客户端只能发送认证请求（包括 ping 在内的其他消息一律回复认证错误）
	if msg.Type != MsgTypeAuth && !c.authenticated {
		c.sendAuthError()
		return true
	}

	switch msg.Type {
	case MsgTypeAuth:
		if !c.handleAuth(msg) {
			// 认证失败：结果已入队，readPump 退出后由 writePump 写出再关闭连接
			return false
		}

	case MsgTypePing:
		// 兼容旧客户端的应用层心跳（新客户端依赖 WebSocket 控制帧 ping/pong）
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
		// 处理订阅更新
		var req SubscribeRequest
		if err := msg.ParseData(&req); err != nil {
			slog.Warn("无效的订阅请求数据", "client_id", c.ID, "error", err)
			return true
		}
		c.hub.UpdateSubscription(c, req.Domains)
		slog.Debug("客户端订阅更新请求已处理", "client_id", c.ID, "domains", req.Domains)

	case MsgTypeCertRequest:
		// 处理证书请求（CLI 模式）
		c.handleCertRequest(msg)

	case MsgTypeStatusRequest:
		// 处理状态请求（CLI 模式）
		c.handleStatusRequest(msg)

	case MsgTypeSyncRequest:
		// 处理证书同步请求（Daemon 模式）
		c.handleSyncRequest(msg)
	}
	return true
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
		case data, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// send 已关闭（Hub 注销或未认证连接退出）
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
				slog.Warn("WebSocket 写入失败", "client_id", c.ID, "error", err)
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// sendMessage 序列化消息并放入发送队列（仅由 readPump 所在协程调用，此时 send 未关闭）
// 队列已满时丢弃，避免 writePump 退出后阻塞 readPump
func (c *Client) sendMessage(msg *Message) {
	data, err := json.Marshal(msg)
	if err != nil {
		slog.Error("消息序列化失败", "error", err)
		return
	}

	select {
	case c.send <- data:
	default:
		slog.Warn("发送缓冲区已满，丢弃消息", "client_id", c.ID, "type", msg.Type)
	}
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

	slog.Debug("处理证书请求", "client_id", c.ID, "domain", req.Domain)

	files, err := c.certs.Load(req.Domain)
	switch {
	case errors.Is(err, cert.ErrInvalidDomain):
		c.sendCertResponse(req.Domain, nil, 0, "域名非法")
		return
	case errors.Is(err, cert.ErrDomainNotFound):
		c.sendCertResponse(req.Domain, nil, 0, "域名不存在")
		return
	case err != nil:
		c.sendCertResponse(req.Domain, nil, 0, "没有可用的证书文件")
		return
	}

	c.sendCertResponse(req.Domain, files, cert.ParseTimeLog(files["time.log"]), "")
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
	// 同一域名可能命中多个订阅项（如 "*" 与 "a.example.com"），只处理一次
	seen := make(map[string]bool)

	// 遍历客户端订阅的域名（通配订阅与实时推送一致，展开为证书目录下所有匹配的域名）
	for _, pattern := range c.domains {
		domains, err := c.certs.Match(pattern)
		if err != nil {
			slog.Warn("读取证书目录失败", "error", err)
			continue
		}
		for _, domain := range domains {
			if c.syncDomain(domain, req.Timestamps, seen) {
				pushedCount++
			}
		}
	}

	slog.Info("证书同步请求处理完成", "client_id", c.ID, "pushed", pushedCount)
}

// syncDomain 服务端时间戳新于客户端（客户端未提供视为 0）时推送该域名，返回是否已推送
func (c *Client) syncDomain(domain string, clientTimestamps map[string]int64, seen map[string]bool) bool {
	if seen[domain] {
		return false
	}
	seen[domain] = true

	// 服务端无此域名证书时 serverTS 为 0，不推送
	serverTS := c.certs.Timestamp(domain)
	if serverTS == 0 || serverTS <= clientTimestamps[domain] {
		return false
	}
	return c.pushCertToDomain(domain)
}

// pushCertToDomain 推送指定域名的证书给当前客户端
func (c *Client) pushCertToDomain(domain string) bool {
	files, err := c.certs.Load(domain)
	if err != nil {
		return false
	}

	data := &CertPushData{
		Domain:    domain,
		Files:     files,
		Timestamp: cert.ParseTimeLog(files["time.log"]),
	}

	msg, err := NewMessage(MsgTypeCertPush, data)
	if err != nil {
		return false
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return false
	}

	// 发送消息
	select {
	case c.send <- payload:
		slog.Debug("同步推送证书", "client_id", c.ID, "domain", domain)
		return true
	default:
		slog.Warn("同步推送证书失败：发送缓冲区已满", "client_id", c.ID, "domain", domain)
		return false
	}
}
