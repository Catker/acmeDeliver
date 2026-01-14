// Package websocket 提供 WebSocket 通信层，用于服务端和客户端之间的双向实时通信
package websocket

import (
	"encoding/json"
	"time"

	"github.com/Catker/acmeDeliver/pkg/cert"
)

// DomainStatus 是 cert.DomainStatus 的类型别名，保持 API 兼容性
type DomainStatus = cert.DomainStatus

// 消息类型常量
const (
	MsgTypeAuth       = "auth"        // 客户端认证请求
	MsgTypeAuthResult = "auth_result" // 认证结果响应
	MsgTypeSubscribe  = "subscribe"   // 订阅域名
	MsgTypeCertPush   = "cert_push"   // 推送证书
	MsgTypeCertAck    = "cert_ack"    // 证书接收确认
	MsgTypePing       = "ping"        // 心跳请求
	MsgTypePong       = "pong"        // 心跳响应
	MsgTypeError      = "error"       // 错误消息

	// CLI 一次性操作消息类型
	MsgTypeCertRequest    = "cert_request"    // 请求下载证书
	MsgTypeCertResponse   = "cert_response"   // 证书响应
	MsgTypeStatusRequest  = "status_request"  // 请求服务器状态
	MsgTypeStatusResponse = "status_response" // 状态响应

	// Daemon 模式证书同步
	MsgTypeSyncRequest = "sync_request" // 证书同步请求（客户端发送本地时间戳，服务端推送差异证书）
)

// Message WebSocket 消息结构
type Message struct {
	Type      string          `json:"type"`
	Timestamp int64           `json:"timestamp"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// NewMessage 创建新消息
func NewMessage(msgType string, data interface{}) (*Message, error) {
	var rawData json.RawMessage
	if data != nil {
		bytes, err := json.Marshal(data)
		if err != nil {
			return nil, err
		}
		rawData = bytes
	}
	return &Message{
		Type:      msgType,
		Timestamp: time.Now().Unix(),
		Data:      rawData,
	}, nil
}

// AuthRequest 认证请求数据
type AuthRequest struct {
	ClientID  string   `json:"client_id"` // 客户端标识
	Signature string   `json:"signature"` // 签名 = sha256(password + timestamp)
	Domains   []string `json:"domains"`   // 订阅的域名列表
}

// AuthResponse 认证响应数据
type AuthResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}

// CertPushData 证书推送数据
type CertPushData struct {
	Domain    string            `json:"domain"`    // 域名
	Files     map[string][]byte `json:"files"`     // 文件名 -> 文件内容
	Timestamp int64             `json:"timestamp"` // 证书更新时间戳
}

// CertAck 证书接收确认
type CertAck struct {
	Domain  string `json:"domain"`
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}

// SubscribeRequest 订阅请求数据（用于动态更新订阅）
type SubscribeRequest struct {
	Domains []string `json:"domains"` // 新的订阅域名列表
}

// ErrorData 错误消息数据
type ErrorData struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ParseData 解析消息数据到指定类型
func (m *Message) ParseData(v interface{}) error {
	if m.Data == nil {
		return nil
	}
	return json.Unmarshal(m.Data, v)
}

// ============================================
// CLI 一次性操作的请求/响应数据结构
// ============================================

// CertRequest CLI 模式证书请求
type CertRequest struct {
	Domain string `json:"domain"`          // 请求的域名
	Force  bool   `json:"force,omitempty"` // 强制更新（忽略时间戳检查）
}

// CertResponse 证书响应
type CertResponse struct {
	Domain    string            `json:"domain"`              // 域名
	Files     map[string][]byte `json:"files,omitempty"`     // 文件名 -> 文件内容
	Timestamp int64             `json:"timestamp,omitempty"` // 证书更新时间戳
	Error     string            `json:"error,omitempty"`     // 错误信息
}

// StatusRequest 状态请求（空请求体）
type StatusRequest struct{}

// ClientStatusInfo 客户端状态信息
type ClientStatusInfo struct {
	ID          string   `json:"id"`           // 客户端 ID
	RemoteIP    string   `json:"remote_ip"`    // 客户端 IP
	ConnectedAt int64    `json:"connected_at"` // 连接时间戳
	Domains     []string `json:"domains"`      // 订阅的域名
}

// StatusResponse 状态响应
type StatusResponse struct {
	GeneratedAt int64              `json:"generated_at"`      // 状态生成时间戳
	Clients     []ClientStatusInfo `json:"clients"`           // 在线客户端列表
	Domains     []DomainStatus     `json:"domains,omitempty"` // 证书状态列表
	Error       string             `json:"error,omitempty"`   // 错误信息
}

// SyncRequest 证书同步请求数据
// 客户端发送本地各域名的时间戳，服务端比对后推送差异证书
type SyncRequest struct {
	Timestamps map[string]int64 `json:"timestamps"` // 域名 -> 本地时间戳（0 表示本地无此证书）
}
