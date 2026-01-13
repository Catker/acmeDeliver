package websocket

import (
	"log/slog"
	"sync"
	"time"
)

// Hub 客户端连接管理中心
// 维护所有在线客户端连接，提供按域名查找订阅者的能力
type Hub struct {
	// 所有已认证的客户端连接
	clients map[*Client]bool

	// 域名 -> 订阅该域名的客户端列表
	subscriptions map[string]map[*Client]bool

	// 客户端注册通道
	register chan *Client

	// 客户端注销通道
	unregister chan *Client

	// 互斥锁
	mu sync.RWMutex
}

// NewHub 创建新的 Hub
func NewHub() *Hub {
	return &Hub{
		clients:       make(map[*Client]bool),
		subscriptions: make(map[string]map[*Client]bool),
		register:      make(chan *Client),
		unregister:    make(chan *Client),
	}
}

// Run 运行 Hub 主循环
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.registerClient(client)
		case client := <-h.unregister:
			h.unregisterClient(client)
		}
	}
}

// registerClient 注册客户端
func (h *Hub) registerClient(client *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.clients[client] = true

	// 为客户端订阅的域名建立索引
	for _, domain := range client.domains {
		if h.subscriptions[domain] == nil {
			h.subscriptions[domain] = make(map[*Client]bool)
		}
		h.subscriptions[domain][client] = true
	}

	slog.Info("客户端已连接",
		"client_id", client.ID,
		"domains", client.domains,
		"total_clients", len(h.clients))
}

// unregisterClient 注销客户端
func (h *Hub) unregisterClient(client *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if _, ok := h.clients[client]; !ok {
		return
	}

	// 从域名订阅中移除
	for _, domain := range client.domains {
		if subs, ok := h.subscriptions[domain]; ok {
			delete(subs, client)
			if len(subs) == 0 {
				delete(h.subscriptions, domain)
			}
		}
	}

	delete(h.clients, client)
	close(client.send)

	slog.Info("客户端已断开",
		"client_id", client.ID,
		"total_clients", len(h.clients))
}

// UpdateSubscription 更新客户端订阅的域名
func (h *Hub) UpdateSubscription(client *Client, newDomains []string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// 从旧的订阅中移除
	for _, domain := range client.domains {
		if subs, ok := h.subscriptions[domain]; ok {
			delete(subs, client)
			if len(subs) == 0 {
				delete(h.subscriptions, domain)
			}
		}
	}

	// 更新客户端的域名列表
	client.domains = newDomains

	// 添加到新的订阅
	for _, domain := range client.domains {
		if h.subscriptions[domain] == nil {
			h.subscriptions[domain] = make(map[*Client]bool)
		}
		h.subscriptions[domain][client] = true
	}

	slog.Info("客户端订阅已更新",
		"client_id", client.ID,
		"domains", client.domains)
}

// Register 注册客户端 (外部调用)
func (h *Hub) Register(client *Client) {
	h.register <- client
}

// Unregister 注销客户端 (外部调用)
func (h *Hub) Unregister(client *Client) {
	h.unregister <- client
}

// ClientStatus 客户端状态信息（用于外部查询）
type ClientStatus struct {
	ID          string    // 客户端 ID
	RemoteIP    string    // 客户端 IP
	ConnectedAt time.Time // 连接时间
	Domains     []string  // 订阅的域名
}

// GetClientStatus 获取所有在线客户端状态
func (h *Hub) GetClientStatus() []ClientStatus {
	h.mu.RLock()
	defer h.mu.RUnlock()

	result := make([]ClientStatus, 0, len(h.clients))
	for client := range h.clients {
		result = append(result, ClientStatus{
			ID:          client.ID,
			RemoteIP:    client.RemoteIP,
			ConnectedAt: client.ConnectedAt,
			Domains:     client.domains,
		})
	}
	return result
}

// GetSubscribers 获取订阅指定域名的所有客户端
func (h *Hub) GetSubscribers(domain string) []*Client {
	h.mu.RLock()
	defer h.mu.RUnlock()

	// 使用 map 去重 - O(1) 查找复杂度
	clientSet := make(map[*Client]struct{})

	// 精确匹配
	if subs, ok := h.subscriptions[domain]; ok {
		for client := range subs {
			clientSet[client] = struct{}{}
		}
	}

	// 通配符匹配 (*.example.com)
	for pattern, subs := range h.subscriptions {
		if matchWildcard(pattern, domain) {
			for client := range subs {
				clientSet[client] = struct{}{} // 自动去重
			}
		}
	}

	// 转换为 slice 返回
	clients := make([]*Client, 0, len(clientSet))
	for client := range clientSet {
		clients = append(clients, client)
	}

	return clients
}

// BroadcastCert 向订阅指定域名的所有客户端推送证书
func (h *Hub) BroadcastCert(domain string, data *CertPushData) int {
	subscribers := h.GetSubscribers(domain)
	if len(subscribers) == 0 {
		slog.Debug("没有客户端订阅此域名", "domain", domain)
		return 0
	}

	msg, err := NewMessage(MsgTypeCertPush, data)
	if err != nil {
		slog.Error("创建推送消息失败", "error", err)
		return 0
	}

	sent := 0
	for _, client := range subscribers {
		select {
		case client.send <- msg:
			sent++
		default:
			// 客户端发送缓冲区已满，跳过
			slog.Warn("客户端发送缓冲区已满，跳过推送",
				"client_id", client.ID,
				"domain", domain)
		}
	}

	slog.Info("证书推送完成",
		"domain", domain,
		"subscribers", len(subscribers),
		"sent", sent)

	return sent
}

// matchWildcard 检查域名是否匹配通配符模式
// 支持 *.example.com 形式的通配符
func matchWildcard(pattern, domain string) bool {
	if len(pattern) < 2 || pattern[0] != '*' || pattern[1] != '.' {
		return false
	}

	suffix := pattern[1:] // .example.com
	if len(domain) <= len(suffix) {
		return false
	}

	// 检查域名是否以 .example.com 结尾
	return domain[len(domain)-len(suffix):] == suffix
}
