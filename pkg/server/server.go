// Package server 提供服务端功能
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Catker/acmeDeliver/pkg/config"
	"github.com/Catker/acmeDeliver/pkg/handler"
	"github.com/Catker/acmeDeliver/pkg/security"
	"github.com/Catker/acmeDeliver/pkg/watcher"
	"github.com/Catker/acmeDeliver/pkg/websocket"
)

// Server 服务器实例，封装所有依赖
// 通过依赖注入替代全局变量，提升可测试性
type Server struct {
	hub       *websocket.Hub
	config    *config.Config
	whitelist *security.IPWhitelist
	watcher   *watcher.CertWatcher
}

// NewServer 创建服务器实例
func NewServer(cfg *config.Config) (*Server, error) {
	// 初始化 WebSocket Hub
	hub := websocket.NewHub()
	go hub.Run()
	slog.Info("📡 WebSocket Hub 已启动")

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

	return srv, nil
}

// Run 启动服务器（阻塞直到收到关闭信号）
func (s *Server) Run(ctx context.Context) error {
	cfg := s.config

	// 注册配置热重载回调 - 更新白名单
	config.RegisterReloadCallback(func(newCfg *config.Config) {
		s.whitelist.Update(newCfg.IPWhitelist)
		if s.whitelist.IsEnabled() {
			slog.Info("🔄 IP 白名单已更新", "whitelist", newCfg.IPWhitelist)
		} else {
			slog.Info("🔓 IP 白名单已禁用")
		}
	})

	// 设置证书变更回调 - 推送到订阅的客户端
	s.watcher.OnChange(func(domain string, files map[string][]byte) {
		// 从 time.log 读取实际时间戳，保持与服务端一致
		var timestamp int64
		if timeContent, ok := files["time.log"]; ok {
			ts := string(timeContent)
			// 只取前10位（Unix 时间戳）
			if len(ts) > 10 {
				ts = ts[:10]
			}
			if t, err := strconv.ParseInt(strings.TrimSpace(ts), 10, 64); err == nil {
				timestamp = t
			}
		}
		// 如果没有 time.log 或解析失败，使用当前时间
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

	// 启动证书监控
	if err := s.watcher.Start(); err != nil {
		return err
	}
	slog.Info("👀 证书目录监控已启动", "dir", cfg.BaseDir)

	// 设置路由
	mux := http.NewServeMux()
	mux.HandleFunc("/", handler.HandleHome)

	// WebSocket 端点
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		// 读取最新配置以支持 trust_proxy 热重载
		currentCfg := config.GetConfig()
		trustProxy := cfg.TrustProxy
		if currentCfg != nil {
			trustProxy = currentCfg.TrustProxy
		}
		websocket.ServeWs(s.hub, cfg.Key, cfg.BaseDir, s.whitelist, trustProxy, w, r)
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
			Addr:    tlsAddr,
			Handler: mux,
		}
		go func() {
			slog.Info("🔒 TLS服务器启动", "addr", "https://"+tlsAddr)
			if err := tlsServer.ListenAndServeTLS(cfg.CertFile, cfg.KeyFile); err != nil && err != http.ErrServerClosed {
				slog.Error("TLS服务器启动失败", "error", err)
				errChan <- fmt.Errorf("TLS服务器启动失败: %w", err)
			}
		}()
	}

	// 设置优雅关闭信号处理
	shutdownChan := make(chan os.Signal, 1)
	signal.Notify(shutdownChan, syscall.SIGINT, syscall.SIGTERM)

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

	// 等待关闭信号或启动错误
	var sig os.Signal
	select {
	case err := <-errChan:
		return err
	case sig = <-shutdownChan:
		slog.Info("🛑 收到信号，开始优雅关闭...", "signal", sig)
	}

	// 创建关闭超时上下文
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 使用 GracefulShutdown 管理关闭序列
	shutdown := NewGracefulShutdown()

	// 添加 HTTP 服务器
	shutdown.AddFunc("HTTP服务器", httpServer.Shutdown)

	// 添加 TLS 服务器（如果启用）
	if tlsServer != nil {
		shutdown.AddFunc("TLS服务器", tlsServer.Shutdown)
	}

	// 添加证书监控
	shutdown.AddFunc("证书监控", func(ctx context.Context) error {
		return s.watcher.Stop()
	})

	// 执行优雅关闭
	shutdown.Shutdown(shutdownCtx)

	slog.Info("✅ 服务已优雅关闭")
	return nil
}
