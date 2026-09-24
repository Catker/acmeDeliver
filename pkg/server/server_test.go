package server

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Catker/acmeDeliver/pkg/config"
)

func TestServerRun_RespectsContextCancellation(t *testing.T) {
	tmpDir := t.TempDir()

	srv, err := NewServer(&config.Config{
		Bind:    "127.0.0.1",
		Port:    "0",
		BaseDir: tmpDir,
		Key:     "test-key",
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		done <- srv.Run(ctx)
	}()

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run(ctx) error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run(ctx) 未在上下文取消后及时退出")
	}
}

func TestPlainHTTPEnabled(t *testing.T) {
	tests := []struct {
		name     string
		tls      bool
		keepHTTP bool
		want     bool
	}{
		{"未启用 TLS 监听明文", false, false, true},
		{"未启用 TLS 时 keep 无影响", false, true, true},
		{"启用 TLS 默认不监听明文", true, false, false},
		{"启用 TLS 且 tls_keep_http 同时监听明文", true, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{TLS: tt.tls, TLSKeepHTTP: tt.keepHTTP}
			if got := plainHTTPEnabled(cfg); got != tt.want {
				t.Errorf("plainHTTPEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

// newTLSServerOnBusyPort 预先占用一个明文端口，返回以该端口为 port、启用 TLS 的服务器
// 若 Run 尝试监听明文端口，会因端口被占用而返回错误
func newTLSServerOnBusyPort(t *testing.T, keepHTTP bool) *Server {
	t.Helper()
	tmpDir := t.TempDir()
	certFile := filepath.Join(tmpDir, "server.crt")
	keyFile := filepath.Join(tmpDir, "server.key")
	writeSelfSignedPair(t, certFile, keyFile, "localhost")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	_, busyPort, _ := net.SplitHostPort(ln.Addr().String())

	srv, err := NewServer(&config.Config{
		Bind:        "127.0.0.1",
		Port:        busyPort,
		BaseDir:     tmpDir,
		Key:         "test-key",
		TLS:         true,
		TLSPort:     "0",
		TLSKeepHTTP: keepHTTP,
		CertFile:    certFile,
		KeyFile:     keyFile,
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	return srv
}

// 启用 TLS 且未设置 tls_keep_http：不监听明文端口，httpServer 为 nil 时也能优雅关闭
func TestServerRun_TLSOnlySkipsPlainPort(t *testing.T) {
	srv := newTLSServerOnBusyPort(t, false)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- srv.Run(ctx)
	}()

	select {
	case err := <-done:
		t.Fatalf("Run(ctx) 不应尝试监听明文端口，却提前返回: %v", err)
	case <-time.After(300 * time.Millisecond):
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run(ctx) error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run(ctx) 未在上下文取消后及时退出")
	}
}

// 启用 TLS 且 tls_keep_http: true：仍监听明文端口（端口被占用时启动失败）
func TestServerRun_TLSKeepHTTPListensPlainPort(t *testing.T) {
	srv := newTLSServerOnBusyPort(t, true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- srv.Run(ctx)
	}()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "HTTP服务器启动失败") {
			t.Fatalf("Run(ctx) error = %v, want HTTP服务器启动失败", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("tls_keep_http 开启时应尝试监听明文端口")
	}
}

func TestIsLoopbackBind(t *testing.T) {
	tests := []struct {
		bind string
		want bool
	}{
		{"", false},
		{"0.0.0.0", false},
		{"::", false},
		{"192.168.1.10", false},
		{"example.com", false},
		{"127.0.0.1", true},
		{"127.1.2.3", true},
		{"::1", true},
		{"[::1]", true},
		{"localhost", true},
		{"LOCALHOST", true},
	}
	for _, tt := range tests {
		if got := isLoopbackBind(tt.bind); got != tt.want {
			t.Errorf("isLoopbackBind(%q) = %v, want %v", tt.bind, got, tt.want)
		}
	}
}

func TestPlaintextExposed(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Config
		want bool
	}{
		{"明文监听所有网卡告警", config.Config{Bind: ""}, true},
		{"明文监听 0.0.0.0 告警", config.Config{Bind: "0.0.0.0"}, true},
		{"明文仅监听回环不告警", config.Config{Bind: "127.0.0.1"}, false},
		{"启用 TLS 不告警", config.Config{Bind: "", TLS: true}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := plaintextExposed(&tt.cfg); got != tt.want {
				t.Errorf("plaintextExposed() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWeakKey(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{"", true},
		{"mypassword", true},
		{"123456789012345", true},
		{"1234567890123456", false},
		{"3f1c9a2e-5b7d-4e8f-9a0b-1c2d3e4f5a6b", false}, // 自动生成的 UUID
	}
	for _, tt := range tests {
		if got := weakKey(tt.key); got != tt.want {
			t.Errorf("weakKey(%q) = %v, want %v", tt.key, got, tt.want)
		}
	}
}
