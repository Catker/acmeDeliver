// Package server 提供服务端状态管理功能
package server

import (
	"time"

	"github.com/Catker/acmeDeliver/pkg/cert"
	"github.com/Catker/acmeDeliver/pkg/websocket"
)

// ServerStatus 服务器状态信息
type ServerStatus struct {
	GeneratedAt time.Time                `json:"generated_at"` // 状态生成时间
	Clients     []websocket.ClientStatus `json:"clients"`      // 在线客户端列表
	Domains     []DomainCertStatus       `json:"domains"`      // 证书状态列表
}

// DomainCertStatus 是 cert.DomainStatus 的类型别名，保持 API 兼容性
type DomainCertStatus = cert.DomainStatus

// CollectStatus 收集服务器状态
func CollectStatus(hub *websocket.Hub, certDir string) *ServerStatus {
	status := &ServerStatus{
		GeneratedAt: time.Now(),
		Clients:     hub.GetClientStatus(),
		Domains:     collectCertStatus(certDir),
	}
	return status
}

// collectCertStatus 收集证书目录状态
func collectCertStatus(baseDir string) []DomainCertStatus {
	return cert.CollectAllDomainStatus(baseDir)
}
