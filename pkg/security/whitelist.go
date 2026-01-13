package security

import (
	"net"
	"strings"
	"sync"
)

// IPWhitelist IP白名单管理器
type IPWhitelist struct {
	mu      sync.RWMutex
	enabled bool
	ips     map[string]bool
	cidrs   []*net.IPNet
}

// NewIPWhitelist 创建IP白名单
func NewIPWhitelist(whitelist string) *IPWhitelist {
	wl := &IPWhitelist{
		ips:   make(map[string]bool),
		cidrs: make([]*net.IPNet, 0),
	}

	if whitelist != "" {
		wl.enabled = true
		wl.parseWhitelist(whitelist)
	}

	return wl
}

// parseWhitelist 解析白名单配置
func (wl *IPWhitelist) parseWhitelist(whitelist string) {
	entries := strings.Split(whitelist, ",")
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		// 尝试解析为CIDR
		if strings.Contains(entry, "/") {
			_, ipNet, err := net.ParseCIDR(entry)
			if err == nil {
				wl.cidrs = append(wl.cidrs, ipNet)
				continue
			}
		}

		// 单个IP地址
		wl.ips[entry] = true
	}
}

// IsAllowed 检查IP是否在白名单中
func (wl *IPWhitelist) IsAllowed(ip string) bool {
	if !wl.enabled {
		return true
	}

	wl.mu.RLock()
	defer wl.mu.RUnlock()

	// 检查单个IP
	if wl.ips[ip] {
		return true
	}

	// 检查CIDR网段
	parsedIP := net.ParseIP(ip)
	if parsedIP == nil {
		return false
	}

	for _, ipNet := range wl.cidrs {
		if ipNet.Contains(parsedIP) {
			return true
		}
	}

	return false
}

// Update 更新白名单配置
func (wl *IPWhitelist) Update(whitelist string) {
	wl.mu.Lock()
	defer wl.mu.Unlock()

	wl.ips = make(map[string]bool)
	wl.cidrs = make([]*net.IPNet, 0)

	if whitelist == "" {
		wl.enabled = false
	} else {
		wl.enabled = true
		wl.parseWhitelist(whitelist)
	}
}

// IsEnabled 检查白名单是否启用
func (wl *IPWhitelist) IsEnabled() bool {
	wl.mu.RLock()
	defer wl.mu.RUnlock()
	return wl.enabled
}
