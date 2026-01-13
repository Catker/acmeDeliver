package security

import (
	"testing"
)

func TestNewIPWhitelist(t *testing.T) {
	wl := NewIPWhitelist("192.168.1.0/24,10.0.0.0/8")
	if wl == nil {
		t.Fatal("NewIPWhitelist() returned nil")
	}

	if !wl.IsEnabled() {
		t.Error("IsEnabled() should return true when whitelist is configured")
	}
}

func TestIPWhitelist_IsAllowed(t *testing.T) {
	wl := NewIPWhitelist("192.168.1.0/24")

	// 测试在白名单内的IP
	if !wl.IsAllowed("192.168.1.100") {
		t.Error("IsAllowed() should return true for IP in whitelist")
	}

	// 测试不在白名单内的IP
	if wl.IsAllowed("10.0.0.1") {
		t.Error("IsAllowed() should return false for IP not in whitelist")
	}

	// 测试空的 whitelist
	wlEmpty := NewIPWhitelist("")
	if wlEmpty.IsEnabled() {
		t.Error("IsEnabled() should return false for empty whitelist")
	}
}

func TestIPWhitelist_Update(t *testing.T) {
	wl := NewIPWhitelist("192.168.1.0/24")

	// 更新白名单
	wl.Update("10.0.0.0/8")

	// 验证新白名单生效
	if !wl.IsAllowed("10.0.0.1") {
		t.Error("IsAllowed() should return true for IP in updated whitelist")
	}

	if wl.IsAllowed("192.168.1.100") {
		t.Error("IsAllowed() should return false for IP not in updated whitelist")
	}
}
