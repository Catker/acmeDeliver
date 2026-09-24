package websocket

import (
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/Catker/acmeDeliver/pkg/cert"
)

// Hub 客户端连接管理中心
// 维护所有在线客户端连接，提供按域名查找订阅者的能力
type Hub struct {
	clients       map[*Client]bool            // 所有已认证的客户端连接
	subscriptions map[string]map[*Client]bool // 域名 -> 订阅该域名的客户端
	mu            sync.RWMutex
}

// NewHub 创建新的 Hub
func NewHub() *Hub {
	return &Hub{
		clients:       make(map[*Client]bool),
		subscriptions: make(map[string]map[*Client]bool),
	}
}

// Register 注册已认证的客户端，并为其订阅的域名建立索引
func (h *Hub) Register(client *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.clients[client] = true
	h.subscribe(client)

	slog.Info("客户端已连接",
		"client_id", client.ID,
		"domains", client.domains,
		"total_clients", len(h.clients))
}

// Unregister 注销客户端并关闭其 send 通道（写锁内关闭，与 BroadcastCert 读锁内发送互斥）
func (h *Hub) Unregister(client *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if !h.clients[client] {
		return
	}
	h.unsubscribe(client)
	delete(h.clients, client)
	close(client.send)

	slog.Info("客户端已断开",
		"client_id", client.ID,
		"total_clients", len(h.clients))
}

// subscribe 将客户端加入其 domains 的订阅索引（调用方须持有写锁）
func (h *Hub) subscribe(client *Client) {
	for _, domain := range client.domains {
		if h.subscriptions[domain] == nil {
			h.subscriptions[domain] = make(map[*Client]bool)
		}
		h.subscriptions[domain][client] = true
	}
}

// unsubscribe 将客户端从其 domains 的订阅索引移除（调用方须持有写锁）
func (h *Hub) unsubscribe(client *Client) {
	for _, domain := range client.domains {
		if subs, ok := h.subscriptions[domain]; ok {
			delete(subs, client)
			if len(subs) == 0 {
				delete(h.subscriptions, domain)
			}
		}
	}
}

// UpdateSubscription 更新客户端订阅的域名
func (h *Hub) UpdateSubscription(client *Client, newDomains []string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.unsubscribe(client)
	client.domains = newDomains
	h.subscribe(client)

	slog.Info("客户端订阅已更新",
		"client_id", client.ID,
		"domains", client.domains)
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

// getSubscribers 获取订阅指定域名的所有客户端（调用方须持有 h.mu 读锁）
// 支持三种匹配模式：
// 1. 精确匹配：domain == "example.com"
// 2. 通配符匹配：pattern == "*.example.com" 匹配 "api.example.com"
// 3. 全局订阅：pattern == "*" 匹配所有域名
func (h *Hub) getSubscribers(domain string) []*Client {
	// 使用 map 去重 - O(1) 查找复杂度
	clientSet := make(map[*Client]struct{})

	// 全局订阅匹配 ("*" 订阅所有域名)
	if subs, ok := h.subscriptions["*"]; ok {
		for client := range subs {
			clientSet[client] = struct{}{}
		}
	}

	// 精确匹配
	if subs, ok := h.subscriptions[domain]; ok {
		for client := range subs {
			clientSet[client] = struct{}{}
		}
	}

	// 通配符匹配 (*.example.com)
	for pattern, subs := range h.subscriptions {
		if cert.MatchWildcard(pattern, domain) {
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
// 查找订阅者与非阻塞发送都在读锁内完成：unregisterClient 需写锁才能 close(send)，
// 两者互斥，避免向已关闭的 send 通道发送导致 panic
func (h *Hub) BroadcastCert(domain string, data *CertPushData) int {
	// 只序列化一次，所有订阅者共享同一份 []byte（只读）
	msg, err := NewMessage(MsgTypeCertPush, data)
	if err != nil {
		slog.Error("创建推送消息失败", "error", err)
		return 0
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		slog.Error("序列化推送消息失败", "error", err)
		return 0
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	subscribers := h.getSubscribers(domain)
	if len(subscribers) == 0 {
		slog.Debug("没有客户端订阅此域名", "domain", domain)
		return 0
	}

	sent := 0
	for _, client := range subscribers {
		select {
		case client.send <- payload:
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
