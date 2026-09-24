// Package server 提供服务端功能
package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Catker/acmeDeliver/pkg/cert"
	"github.com/Catker/acmeDeliver/pkg/config"
	"github.com/Catker/acmeDeliver/pkg/security"
	"github.com/Catker/acmeDeliver/pkg/watcher"
	"github.com/Catker/acmeDeliver/pkg/websocket"
)

// readHeaderTimeout 读取请求头的时限，防止慢速请求头（slowloris）长期占用连接；
// 不设置 ReadTimeout/WriteTimeout，它们会断开 WebSocket 长连接
const readHeaderTimeout = 10 * time.Second

// minKeyLength 低于该长度的密钥启动时告警（认证失败无限速，弱密码可被在线爆破）
const minKeyLength = 16

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

// plainHTTPEnabled 判断是否监听明文端口：未启用 TLS 时必须监听；
// 启用 TLS 时仅在显式配置 tls_keep_http 时保留（明文端口会暴露私钥）
func plainHTTPEnabled(cfg *config.Config) bool {
	return !cfg.TLS || cfg.TLSKeepHTTP
}

// isLoopbackBind 判断监听地址是否仅限本机回环（localhost、127.0.0.0/8、::1）；
// 空地址、0.0.0.0、:: 等监听所有网卡，视为非回环
func isLoopbackBind(bind string) bool {
	if strings.EqualFold(bind, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(bind, "[]"))
	return ip != nil && ip.IsLoopback()
}

// plaintextExposed 判断是否在非回环地址上明文监听（证书私钥会明文传输）
func plaintextExposed(cfg *config.Config) bool {
	return !cfg.TLS && !isLoopbackBind(cfg.Bind)
}

// weakKey 判断密钥是否过短（自动生成的 UUID 为 36 位，不会触发）
func weakKey(key string) bool {
	return len(key) < minKeyLength
}

// warnInsecureConfig 对不安全的配置打印告警（只提示，不改变行为）
func warnInsecureConfig(cfg *config.Config) {
	if plaintextExposed(cfg) {
		slog.Warn("⚠️ 未启用 TLS 且监听非本机地址：证书私钥与认证签名将明文传输；建议启用 TLS，或只监听 127.0.0.1 并置于 TLS 反向代理之后",
			"bind", cfg.Bind)
	}
	if weakKey(cfg.Key) {
		slog.Warn("⚠️ 密钥过短：认证失败没有限速，弱密码可被在线爆破；建议使用至少 16 位的随机密钥",
			"length", len(cfg.Key), "min", minKeyLength)
	}
}

// Run 启动服务器（阻塞直到上下文取消或启动失败）
func (s *Server) Run(ctx context.Context) error {
	cfg := s.config
	warnInsecureConfig(cfg)

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

	// 错误通道用于 goroutine 错误传递
	errChan := make(chan error, 2)

	// 创建 HTTP 服务器：启用 TLS 时默认不监听明文端口，除非显式配置 tls_keep_http
	var httpServer *http.Server
	if plainHTTPEnabled(cfg) {
		httpAddr := cfg.Bind + ":" + cfg.Port
		httpServer = &http.Server{
			Addr:              httpAddr,
			Handler:           mux,
			ReadHeaderTimeout: readHeaderTimeout,
		}
		if cfg.TLS {
			slog.Warn("⚠️ tls_keep_http 已开启：明文端口同时监听，经此端口传输的证书私钥与认证签名均未加密",
				"addr", "http://"+httpAddr)
		}
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
	} else {
		slog.Info("🔐 已启用 TLS，明文端口不监听（如需兼容旧部署可设置 tls_keep_http: true）")
	}

	// 创建 TLS 服务器（如果启用）
	var tlsServer *http.Server
	if cfg.TLS {
		tlsAddr := cfg.Bind + ":" + cfg.TLSPort
		tlsServer = &http.Server{
			Addr:              tlsAddr,
			Handler:           mux,
			TLSConfig:         &tls.Config{GetCertificate: reloader.GetCertificate},
			ReadHeaderTimeout: readHeaderTimeout,
		}
		go func() {
			slog.Info("🔒 TLS服务器启动", "addr", "https://"+tlsAddr)
			if err := tlsServer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
				slog.Error("TLS服务器启动失败", "error", err)
				errChan <- fmt.Errorf("TLS服务器启动失败: %w", err)
			}
		}()
	}

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
	if httpServer != nil {
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			slog.Warn("⚠️ 关闭 HTTP 服务器失败", "error", err)
		}
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
