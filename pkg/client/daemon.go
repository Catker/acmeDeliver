// Package client 提供客户端功能，包括 daemon 模式
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Catker/acmeDeliver/pkg/config"
	"github.com/Catker/acmeDeliver/pkg/security"
	ws "github.com/Catker/acmeDeliver/pkg/websocket"
)

// DaemonConfig Daemon 模式配置
type DaemonConfig struct {
	ServerURL         string                    // WebSocket 服务器地址
	Password          string                    // 认证密码
	ClientID          string                    // 客户端标识
	WorkDir           string                    // 工作目录
	Subscribe         []string                  // 订阅的域名列表
	Sites             []config.SiteDeployConfig // 站点部署配置
	ReconnectInterval time.Duration             // 重连间隔
	HeartbeatInterval time.Duration             // 心跳间隔
	ReloadDebounce    time.Duration             // Reload 防抖延迟（默认 5 秒）
	TLSConfig         *TLSConfig                // TLS 配置（可选）
}

// Daemon 客户端守护进程
type Daemon struct {
	config *DaemonConfig
	conn   *websocket.Conn
	mu     sync.RWMutex // 保护 config 和 sites 的并发访问
	connMu sync.Mutex   // 保护 conn 写入的并发安全

	// 控制通道
	configUpdates chan *ConfigUpdate // 配置更新通道

	// Reload 防抖器
	reloadDebouncer *ReloadDebouncer

	// Pong 超时检测
	lastPong time.Time
	pongMu   sync.RWMutex
}

// ConfigUpdate 配置更新通知
type ConfigUpdate struct {
	NewSubscribe []string
	NewSites     []config.SiteDeployConfig
}

// NewDaemon 创建新的 Daemon
func NewDaemon(cfg *DaemonConfig) *Daemon {
	// 设置默认防抖延迟
	if cfg.ReloadDebounce <= 0 {
		cfg.ReloadDebounce = 5 * time.Second
	}

	return &Daemon{
		config:          cfg,
		configUpdates:   make(chan *ConfigUpdate, 16),
		reloadDebouncer: NewReloadDebouncer(cfg.ReloadDebounce),
		lastPong:        time.Now(),
	}
}

// backoff 计算指数退避间隔
// attempt 从 0 开始，返回 base * 2^attempt，最大 5 分钟
func backoff(attempt int, base time.Duration) time.Duration {
	const maxBackoff = 5 * time.Minute
	delay := base * time.Duration(1<<uint(attempt))
	if delay > maxBackoff {
		return maxBackoff
	}
	return delay
}

// writeMessage 线程安全的 WebSocket 写入
func (d *Daemon) writeMessage(data []byte) error {
	d.connMu.Lock()
	defer d.connMu.Unlock()
	return d.conn.WriteMessage(websocket.TextMessage, data)
}

// updateLastPong 更新最后收到 pong 的时间
func (d *Daemon) updateLastPong() {
	d.pongMu.Lock()
	d.lastPong = time.Now()
	d.pongMu.Unlock()
}

// getLastPong 获取最后收到 pong 的时间
func (d *Daemon) getLastPong() time.Time {
	d.pongMu.RLock()
	defer d.pongMu.RUnlock()
	return d.lastPong
}

// Run 运行 Daemon
func (d *Daemon) Run(ctx context.Context) error {
	slog.Info("Daemon 模式启动",
		"server", d.config.ServerURL,
		"subscribe", d.config.Subscribe)

	// 确保工作目录存在
	if err := os.MkdirAll(d.config.WorkDir, 0755); err != nil {
		return err
	}

	// 监听系统信号
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	attempt := 0 // 重连尝试次数，用于指数退避
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case sig := <-sigChan:
			slog.Info("收到信号，正在退出", "signal", sig)
			return nil
		default:
			// 连接并处理
			if err := d.connectAndServe(ctx); err != nil {
				slog.Error("连接断开", "error", err)
			} else {
				// 连接成功后重置退避计数
				attempt = 0
			}

			// 计算退避间隔并等待重连
			waitDuration := backoff(attempt, d.config.ReconnectInterval)
			slog.Info("准备重新连接...", "wait", waitDuration, "attempt", attempt+1)

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(waitDuration):
				attempt++
			}
		}
	}
}

// connectAndServe 连接服务器并处理消息
func (d *Daemon) connectAndServe(ctx context.Context) error {
	// 解析服务器地址
	serverURL := d.config.ServerURL
	if !strings.HasPrefix(serverURL, "ws://") && !strings.HasPrefix(serverURL, "wss://") {
		// 将 http:// 转换为 ws://
		serverURL = strings.Replace(serverURL, "http://", "ws://", 1)
		serverURL = strings.Replace(serverURL, "https://", "wss://", 1)
	}
	// 确保所有 URL 都追加 /ws 后缀
	if !strings.HasSuffix(serverURL, "/ws") {
		serverURL = serverURL + "/ws"
	}

	slog.Info("正在连接服务器", "url", serverURL)

	// 构建 TLS 配置
	tlsConfig, err := BuildTLSConfig(d.config.TLSConfig)
	if err != nil {
		return fmt.Errorf("TLS 配置错误: %w", err)
	}

	// 建立连接（带连接超时）
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		TLSClientConfig:  tlsConfig,
	}
	conn, _, err := dialer.DialContext(ctx, serverURL, nil)
	if err != nil {
		return err
	}
	d.conn = conn
	defer conn.Close()

	slog.Info("已连接到服务器")

	// 发送认证请求
	if err := d.authenticate(); err != nil {
		return err
	}

	// 启动心跳
	go d.heartbeat(ctx)

	// 启动配置更新处理
	go d.handleConfigUpdates(ctx)

	// 读取消息循环
	return d.readLoop(ctx)
}

// authenticate 发送认证请求
func (d *Daemon) authenticate() error {
	timestamp := time.Now().Unix()

	// 使用统一的签名验证器生成签名
	verifier := security.NewSignatureVerifier(d.config.Password)
	signature := verifier.GenerateSignature(timestamp)

	authReq := &ws.AuthRequest{
		ClientID:  d.config.ClientID,
		Signature: signature,
		Domains:   d.config.Subscribe,
	}

	msg, err := ws.NewMessage(ws.MsgTypeAuth, authReq)
	if err != nil {
		return err
	}
	msg.Timestamp = timestamp

	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	if err := d.writeMessage(data); err != nil {
		return err
	}

	slog.Debug("已发送认证请求", "client_id", d.config.ClientID, "domains", d.config.Subscribe)
	return nil
}

// readLoop 消息读取循环
func (d *Daemon) readLoop(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			_, data, err := d.conn.ReadMessage()
			if err != nil {
				return err
			}

			var msg ws.Message
			if err := json.Unmarshal(data, &msg); err != nil {
				slog.Warn("无效的消息格式", "error", err)
				continue
			}

			d.handleMessage(&msg)
		}
	}
}

// handleMessage 处理收到的消息
func (d *Daemon) handleMessage(msg *ws.Message) {
	switch msg.Type {
	case ws.MsgTypeAuthResult:
		var resp ws.AuthResponse
		if err := msg.ParseData(&resp); err == nil {
			if resp.Success {
				slog.Info("认证成功", "message", resp.Message)
			} else {
				slog.Error("认证失败", "message", resp.Message)
			}
		}

	case ws.MsgTypeCertPush:
		var certData ws.CertPushData
		if err := msg.ParseData(&certData); err != nil {
			slog.Error("解析证书数据失败", "error", err)
			return
		}
		d.handleCertPush(&certData)

	case ws.MsgTypePong:
		d.updateLastPong()
		slog.Debug("收到心跳响应")

	case ws.MsgTypeError:
		var errData ws.ErrorData
		if err := msg.ParseData(&errData); err == nil {
			slog.Error("收到错误", "code", errData.Code, "message", errData.Message)
		}
	}
}

// handleCertPush 处理证书推送
func (d *Daemon) handleCertPush(data *ws.CertPushData) {
	slog.Info("收到证书推送", "domain", data.Domain, "files", len(data.Files))

	// 1. 保存到工作目录
	domainDir := filepath.Join(d.config.WorkDir, data.Domain)
	if err := os.MkdirAll(domainDir, 0755); err != nil {
		slog.Error("创建域名目录失败", "error", err)
		d.sendCertAck(data.Domain, false, err.Error())
		return
	}

	for filename, content := range data.Files {
		filePath := filepath.Join(domainDir, filename)
		if err := os.WriteFile(filePath, content, 0644); err != nil {
			slog.Error("保存证书文件失败", "file", filePath, "error", err)
			d.sendCertAck(data.Domain, false, err.Error())
			return
		}
		slog.Debug("保存证书文件", "file", filePath)
	}

	slog.Info("证书已保存到工作目录", "dir", domainDir)

	// 2. 查找匹配的站点配置并部署（只复制文件，不执行 reload）
	site := d.findSiteConfig(data.Domain)
	if site != nil {
		if err := d.deployCertFilesWithRetry(data.Domain, domainDir, site, 3); err != nil {
			slog.Error("部署证书失败", "domain", data.Domain, "error", err)
			d.sendCertAck(data.Domain, false, err.Error())
			return
		}
		slog.Info("证书文件部署完成", "domain", data.Domain)

		// 3. 使用 debouncer 触发 reload（防抖）
		if site.ReloadCmd != "" {
			d.reloadDebouncer.Trigger(site.ReloadCmd)
		}
	} else {
		slog.Info("未找到站点配置，跳过自动部署", "domain", data.Domain)
	}

	d.sendCertAck(data.Domain, true, "")
}

// findSiteConfig 查找域名对应的站点配置
func (d *Daemon) findSiteConfig(domain string) *config.SiteDeployConfig {
	for _, site := range d.config.Sites {
		// 精确匹配
		if site.Domain == domain {
			return &site
		}
		// 通配符匹配
		if strings.HasPrefix(site.Domain, "*.") {
			suffix := site.Domain[1:] // .example.com
			if strings.HasSuffix(domain, suffix) {
				return &site
			}
		}
	}
	return nil
}

// deployCertFiles 部署证书文件（只复制文件，不执行 reload）
// reload 命令由调用方通过 debouncer 统一触发
func (d *Daemon) deployCertFiles(domain, srcDir string, site *config.SiteDeployConfig) error {
	// 替换路径中的 {domain} 占位符
	replaceDomain := func(path string) string {
		return strings.ReplaceAll(path, "{domain}", domain)
	}

	// 复制证书文件
	copyFile := func(src, dst string) error {
		if dst == "" {
			return nil
		}
		dst = replaceDomain(dst)

		// 确保目标目录存在
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return err
		}

		content, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, content, 0644)
	}

	// 部署 cert.pem
	if site.CertPath != "" {
		if err := copyFile(filepath.Join(srcDir, "cert.pem"), site.CertPath); err != nil {
			slog.Warn("复制 cert.pem 失败", "error", err)
		}
	}

	// 部署 key.pem
	if site.KeyPath != "" {
		if err := copyFile(filepath.Join(srcDir, "key.pem"), site.KeyPath); err != nil {
			slog.Warn("复制 key.pem 失败", "error", err)
		}
	}

	// 部署 fullchain.pem
	if site.FullchainPath != "" {
		if err := copyFile(filepath.Join(srcDir, "fullchain.pem"), site.FullchainPath); err != nil {
			slog.Warn("复制 fullchain.pem 失败", "error", err)
		}
	}

	return nil
}

// deployCertFilesWithRetry 带重试的证书部署
func (d *Daemon) deployCertFilesWithRetry(domain, srcDir string, site *config.SiteDeployConfig, maxRetries int) error {
	var lastErr error
	for i := 0; i < maxRetries; i++ {
		if err := d.deployCertFiles(domain, srcDir, site); err != nil {
			lastErr = err
			if i < maxRetries-1 {
				slog.Warn("证书部署失败，重试中", "attempt", i+1, "error", err)
				time.Sleep(time.Duration(i+1) * 500 * time.Millisecond)
			}
			continue
		}
		return nil
	}
	return lastErr
}

// sendCertAck 发送证书接收确认
func (d *Daemon) sendCertAck(domain string, success bool, message string) {
	ack := &ws.CertAck{
		Domain:  domain,
		Success: success,
		Message: message,
	}

	msg, err := ws.NewMessage(ws.MsgTypeCertAck, ack)
	if err != nil {
		return
	}

	data, _ := json.Marshal(msg)
	d.writeMessage(data)
}

// heartbeat 心跳发送与 pong 超时检测
func (d *Daemon) heartbeat(ctx context.Context) {
	ticker := time.NewTicker(d.config.HeartbeatInterval)
	defer ticker.Stop()

	d.updateLastPong() // 初始化 pong 时间
	missedPongs := 0
	const maxMissed = 3 // 连续丢失 3 次 pong 则断开

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// 检查 pong 超时
			if time.Since(d.getLastPong()) > d.config.HeartbeatInterval*2 {
				missedPongs++
				slog.Warn("心跳响应超时", "missed", missedPongs)
				if missedPongs >= maxMissed {
					slog.Error("心跳超时，关闭连接")
					d.conn.Close()
					return
				}
			} else {
				missedPongs = 0
			}

			// 发送 ping
			msg, _ := ws.NewMessage(ws.MsgTypePing, nil)
			data, _ := json.Marshal(msg)
			if err := d.writeMessage(data); err != nil {
				slog.Warn("发送心跳失败", "error", err)
				return
			}
			slog.Debug("发送心跳")
		}
	}
}

// ============================================
// 配置热重载相关方法
// ============================================

// handleConfigUpdates 处理配置更新
func (d *Daemon) handleConfigUpdates(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case update := <-d.configUpdates:
			if update == nil {
				continue
			}
			d.applyConfigUpdate(update)
		}
	}
}

// applyConfigUpdate 应用配置更新
func (d *Daemon) applyConfigUpdate(update *ConfigUpdate) {
	d.mu.Lock()
	oldSubscribe := d.config.Subscribe
	d.config.Subscribe = update.NewSubscribe
	d.config.Sites = update.NewSites
	d.mu.Unlock()

	slog.Info("应用配置更新",
		"old_subscribe", oldSubscribe,
		"new_subscribe", update.NewSubscribe,
		"sites_count", len(update.NewSites))

	// 如果订阅列表发生变化，发送新的订阅请求
	if !stringSlicesEqual(oldSubscribe, update.NewSubscribe) {
		if err := d.sendSubscription(update.NewSubscribe); err != nil {
			slog.Error("发送订阅更新失败", "error", err)
		} else {
			slog.Info("订阅更新已发送", "domains", update.NewSubscribe)
		}
	}
}

// UpdateConfig 更新配置（供外部调用）
func (d *Daemon) UpdateConfig(newSubscribe []string, newSites []config.SiteDeployConfig) {
	select {
	case d.configUpdates <- &ConfigUpdate{
		NewSubscribe: newSubscribe,
		NewSites:     newSites,
	}:
	default:
		slog.Warn("配置更新通道已满，跳过此次更新")
	}
}

// sendSubscription 发送订阅请求
func (d *Daemon) sendSubscription(domains []string) error {
	if d.conn == nil {
		return nil
	}

	subReq := &ws.SubscribeRequest{
		Domains: domains,
	}

	msg, err := ws.NewMessage(ws.MsgTypeSubscribe, subReq)
	if err != nil {
		return err
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	return d.writeMessage(data)
}

// stringSlicesEqual 比较两个字符串切片是否相等
func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
