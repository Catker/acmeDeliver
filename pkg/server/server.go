// Package server 提供服务端功能
package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/Catker/acmeDeliver/pkg/cert"
	"github.com/Catker/acmeDeliver/pkg/config"
	"github.com/Catker/acmeDeliver/pkg/security"
	"github.com/Catker/acmeDeliver/pkg/watcher"
	"github.com/Catker/acmeDeliver/pkg/websocket"
)

// Server 服务器实例，封装所有依赖
// 通过依赖注入替代全局变量，提升可测试性
type Server struct {
	hub        *websocket.Hub
	config     *config.Config
	whitelist  *security.IPWhitelist
	watcher    *watcher.CertWatcher
	trustProxy atomic.Bool // 支持热重载
}

// NewServer 创建服务器实例
func NewServer(cfg *config.Config) (*Server, error) {
	// 初始化 WebSocket Hub
	hub := websocket.NewHub()

	// 初始化 IP 白名单
	whitelist := security.NewIPWhitelist(cfg.IPWhitelist)
	if whitelist.IsEnabled() {
		slog.Info("🔒 IP 白名单已启用", "whitelist", cfg.IPWhitelist)
	}

	// 初始化证书目录监控
	certWatcher, err := watcher.NewCertWatcher(cfg.BaseDir, 5*time.Second)
	if err != nil {
		return nil, err
	}

	srv := &Server{
		hub:       hub,
		config:    cfg,
		whitelist: whitelist,
		watcher:   certWatcher,
	}
	srv.trustProxy.Store(cfg.TrustProxy)

	return srv, nil
}

// Run 启动服务器（阻塞直到上下文取消或启动失败）
func (s *Server) Run(ctx context.Context) error {
	cfg := s.config

	// 注册配置热重载回调 - 更新白名单与 trust_proxy
	config.RegisterReloadCallback(func(newCfg *config.Config) {
		s.trustProxy.Store(newCfg.TrustProxy)
		s.whitelist.Update(newCfg.IPWhitelist)
		if s.whitelist.IsEnabled() {
			slog.Info("🔄 IP 白名单已更新", "whitelist", newCfg.IPWhitelist)
		} else {
			slog.Info("🔓 IP 白名单已禁用")
		}
	})

	// 设置证书变更回调 - 推送到订阅的客户端
	s.watcher.OnChange(func(domain string, files map[string][]byte) {
		// 从 time.log 解析时间戳；没有 time.log 或解析失败时使用当前时间
		timestamp := cert.ParseTimeLog(files["time.log"])
		if timestamp == 0 {
			timestamp = time.Now().Unix()
		}

		data := &websocket.CertPushData{
			Domain:    domain,
			Files:     files,
			Timestamp: timestamp,
		}
		sent := s.hub.BroadcastCert(domain, data)
		slog.Info("📤 证书推送", "domain", domain, "clients", sent, "timestamp", timestamp)
	})

	// 启用 TLS 时先加载一次证书，失败即启动失败；之后按文件 mtime 变化热加载
	var reloader *certReloader
	if cfg.TLS {
		var err error
		if reloader, err = newCertReloader(cfg.CertFile, cfg.KeyFile); err != nil {
			return fmt.Errorf("TLS服务器启动失败: %w", err)
		}
	}

	// 启动证书监控
	if err := s.watcher.Start(); err != nil {
		return err
	}
	slog.Info("👀 证书目录监控已启动", "dir", cfg.BaseDir)

	// 设置路由
	mux := http.NewServeMux()
	// 健康检查
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Running"))
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		websocket.ServeWs(s.hub, cfg.Key, cfg.BaseDir, s.whitelist, s.trustProxy.Load(), w, r)
	})

	// 创建 HTTP 服务器
	httpAddr := cfg.Bind + ":" + cfg.Port
	httpServer := &http.Server{
		Addr:    httpAddr,
		Handler: mux,
	}

	// 创建 TLS 服务器（如果启用）
	var tlsServer *http.Server

	// 错误通道用于 goroutine 错误传递
	errChan := make(chan error, 2)

	if cfg.TLS {
		tlsAddr := cfg.Bind + ":" + cfg.TLSPort
		tlsServer = &http.Server{
			Addr:      tlsAddr,
			Handler:   mux,
			TLSConfig: &tls.Config{GetCertificate: reloader.GetCertificate},
		}
		go func() {
			slog.Info("🔒 TLS服务器启动", "addr", "https://"+tlsAddr)
			if err := tlsServer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
				slog.Error("TLS服务器启动失败", "error", err)
				errChan <- fmt.Errorf("TLS服务器启动失败: %w", err)
			}
		}()
	}

	// 启动 HTTP 服务器（非阻塞）
	go func() {
		slog.Info("🚀 HTTP服务器启动",
			"addr", "http://"+httpAddr,
			"certDir", cfg.BaseDir,
			"wsEndpoint", "ws://"+httpAddr+"/ws")
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("HTTP服务器启动失败", "error", err)
			errChan <- fmt.Errorf("HTTP服务器启动失败: %w", err)
		}
	}()

	// 等待上下文取消或启动错误
	select {
	case err := <-errChan:
		return err
	case <-ctx.Done():
		slog.Info("🛑 收到关闭请求，开始优雅关闭...", "reason", ctx.Err())
	}

	// 依次关闭 HTTP、TLS 服务器与证书监控，单项失败只记录日志
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		slog.Warn("⚠️ 关闭 HTTP 服务器失败", "error", err)
	}
	if tlsServer != nil {
		if err := tlsServer.Shutdown(shutdownCtx); err != nil {
			slog.Warn("⚠️ 关闭 TLS 服务器失败", "error", err)
		}
	}
	if err := s.watcher.Stop(); err != nil {
		slog.Warn("⚠️ 关闭证书监控失败", "error", err)
	}

	slog.Info("✅ 服务已优雅关闭")
	return nil
}
