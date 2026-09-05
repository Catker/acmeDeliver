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
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Catker/acmeDeliver/pkg/cert"
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
	SyncInterval      time.Duration             // 定时同步间隔（0/未设置=默认1小时，负数=禁用）
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

	// 使用 signal.NotifyContext 让信号通过 context 传播
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	attempt := 0 // 重连尝试次数，用于指数退避
	for {
		select {
		case <-ctx.Done():
			slog.Info("收到退出信号，正在退出")
			return nil
		default:
			// 连接并处理
			if err := d.connectAndServe(ctx); err != nil {
				// 如果是 context 取消导致的错误，直接返回
				if ctx.Err() != nil {
					slog.Info("收到退出信号，正在退出")
					return nil
				}
				slog.Error("连接断开", "error", err)
			} else {
				// 连接成功后重置退避计数
				attempt = 0
			}

			// 检查是否需要退出
			if ctx.Err() != nil {
				slog.Info("收到退出信号，正在退出")
				return nil
			}

			// 计算退避间隔并等待重连
			waitDuration := backoff(attempt, d.config.ReconnectInterval)
			slog.Info("准备重新连接...", "wait", waitDuration, "attempt", attempt+1)

			select {
			case <-ctx.Done():
				slog.Info("收到退出信号，正在退出")
				return nil
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

	// 启动定时同步（如果配置了 sync_interval）
	go d.syncLoop(ctx)

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
// 使用 goroutine + channel 方式，让读取在后台进行，主循环可以检查退出信号
func (d *Daemon) readLoop(ctx context.Context) error {
	type readResult struct {
		data []byte
		err  error
	}
	resultCh := make(chan readResult, 1)

	// 启动读取 goroutine
	go func() {
		for {
			_, data, err := d.conn.ReadMessage()
			select {
			case resultCh <- readResult{data: data, err: err}:
				if err != nil {
					return // 发生错误时退出 goroutine
				}
			case <-ctx.Done():
				return // context 取消时退出 goroutine
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			// 收到退出信号，关闭连接以解除 ReadMessage 阻塞
			d.conn.Close()
			return ctx.Err()
		case result := <-resultCh:
			if result.err != nil {
				return result.err
			}

			var msg ws.Message
			if err := json.Unmarshal(result.data, &msg); err != nil {
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
				// 认证成功后立即请求同步证书
				if err := d.requestSync(); err != nil {
					slog.Warn("发送证书同步请求失败", "error", err)
				}
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
// 保存或部署失败时发送失败 ACK，不触发 reload；全部成功才发送成功 ACK
func (d *Daemon) handleCertPush(data *ws.CertPushData) {
	slog.Info("收到证书推送", "domain", data.Domain, "files", len(data.Files))

	reloadCmd, err := d.receiveCert(data)
	if err != nil {
		slog.Error("处理证书推送失败", "domain", data.Domain, "error", err)
		d.sendCertAck(data.Domain, false, err.Error())
		return
	}

	// 部署成功后使用 debouncer 触发 reload（防抖）
	if reloadCmd != "" {
		d.reloadDebouncer.Trigger(reloadCmd)
	}

	d.sendCertAck(data.Domain, true, "")
}

// receiveCert 保存证书到工作目录，并部署到匹配的站点目标路径。
// 成功时返回待防抖执行的 reload 命令（无站点配置或未配置 reload 时为空）；
// 任一文件的保存或部署失败都返回错误，调用方不得发送成功 ACK 或触发 reload。
func (d *Daemon) receiveCert(data *ws.CertPushData) (string, error) {
	// 1. 保存到工作目录
	domainDir, err := safeDomainDir(d.config.WorkDir, data.Domain)
	if err != nil {
		return "", fmt.Errorf("非法域名路径: %w", err)
	}
	if err := os.MkdirAll(domainDir, 0755); err != nil {
		return "", fmt.Errorf("创建域名目录失败: %w", err)
	}

	for filename, content := range data.Files {
		filePath, err := safeDomainFilePath(d.config.WorkDir, data.Domain, filename)
		if err != nil {
			return "", fmt.Errorf("非法证书文件路径 %s: %w", filename, err)
		}
		if err := cert.WriteFileAtomic(filePath, content, cert.CertFilePerm(filename)); err != nil {
			return "", fmt.Errorf("保存证书文件 %s 失败: %w", filePath, err)
		}
		slog.Debug("保存证书文件", "file", filePath)
	}

	slog.Info("证书已保存到工作目录", "dir", domainDir)

	// 2. 查找匹配的站点配置并部署（只写文件，reload 由调用方防抖触发）
	d.mu.RLock()
	sites := d.config.Sites
	d.mu.RUnlock()

	site := config.FindSiteConfig(sites, data.Domain)
	if site == nil {
		slog.Info("未找到站点配置，跳过自动部署", "domain", data.Domain)
		return "", nil
	}

	if err := d.deployCertFilesWithRetry(data.Domain, domainDir, site, deployMaxRetries); err != nil {
		return "", fmt.Errorf("部署证书失败: %w", err)
	}
	slog.Info("证书文件部署完成", "domain", data.Domain)

	return site.ReloadCmd, nil
}

// deployMaxRetries 证书部署最大尝试次数：瞬时失败可恢复，持续失败最终返回错误
const deployMaxRetries = 3

// deployRetryBaseDelay 重试的基础退避间隔（第 n 次失败后等待 n*base）；
// 导出约定外的可变量仅用于测试缩短等待
var deployRetryBaseDelay = 500 * time.Millisecond

// deployCertFiles 部署证书文件到站点配置的目标路径（只写文件，不执行 reload）
// reload 命令由调用方通过 debouncer 统一触发
func (d *Daemon) deployCertFiles(domain, srcDir string, site *config.SiteDeployConfig) error {
	// 替换路径中的 {domain} 占位符
	replaceDomain := func(path string) string {
		return strings.ReplaceAll(path, "{domain}", domain)
	}

	// 部署目标：源文件名 -> 站点配置路径
	targets := []struct {
		srcName string
		dstPath string
	}{
		{"cert.pem", site.CertPath},
		{"key.pem", site.KeyPath},
		{"fullchain.pem", site.FullchainPath},
	}

	// 阶段一：预读并校验全部源文件；任一为空整体拒绝，不做部分写入
	type readyTarget struct {
		srcName string
		dst     string
		content []byte
	}
	ready := make([]readyTarget, 0, len(targets))
	for _, t := range targets {
		if t.dstPath == "" {
			continue
		}
		src := filepath.Join(srcDir, t.srcName)
		content, err := os.ReadFile(src)
		if err != nil {
			return fmt.Errorf("读取源文件 %s 失败: %w", src, err)
		}
		dst := replaceDomain(t.dstPath)
		// 与 CLI 共用规则：空内容拒绝部署，避免清空已有目标文件
		if err := cert.CheckDeployContent(t.srcName, content); err != nil {
			return fmt.Errorf("拒绝写入 %s: %w", dst, err)
		}
		ready = append(ready, readyTarget{srcName: t.srcName, dst: dst, content: content})
	}

	// 阶段二：全部校验通过后逐个原子写入
	for _, r := range ready {
		if err := cert.WriteFileAtomic(r.dst, r.content, cert.CertFilePerm(r.srcName)); err != nil {
			return fmt.Errorf("写入 %s 失败: %w", r.dst, err)
		}
	}

	return nil
}

// withRetry 通用重试包装：最多尝试 maxRetries 次，第 n 次失败后线性退避
// 瞬时失败可以恢复，持续失败在用尽次数后返回最后一次错误
func withRetry(maxRetries int, op func() error) error {
	var lastErr error
	for i := 0; i < maxRetries; i++ {
		if err := op(); err != nil {
			lastErr = err
			if i < maxRetries-1 {
				slog.Warn("操作失败，重试中", "attempt", i+1, "error", err)
				time.Sleep(time.Duration(i+1) * deployRetryBaseDelay)
			}
			continue
		}
		return nil
	}
	return lastErr
}

// deployCertFilesWithRetry 带重试的证书部署
func (d *Daemon) deployCertFilesWithRetry(domain, srcDir string, site *config.SiteDeployConfig, maxRetries int) error {
	return withRetry(maxRetries, func() error {
		return d.deployCertFiles(domain, srcDir, site)
	})
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
	if !slices.Equal(oldSubscribe, update.NewSubscribe) {
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

// requestSync 请求同步证书
// 收集本地订阅域名的时间戳，发送给服务端比对
func (d *Daemon) requestSync() error {
	if d.conn == nil {
		return nil
	}

	d.mu.RLock()
	subscribe := d.config.Subscribe
	workDir := d.config.WorkDir
	d.mu.RUnlock()

	timestamps := make(map[string]int64)
	for _, domain := range subscribe {
		if domain == "*" {
			// 全局订阅：收集本地所有域名的时间戳
			d.collectAllLocalTimestamps(workDir, timestamps)
			continue
		}
		ts := d.readLocalTimestamp(workDir, domain)
		timestamps[domain] = ts
	}

	slog.Debug("发送证书同步请求", "domains", len(timestamps))

	req := &ws.SyncRequest{Timestamps: timestamps}
	msg, err := ws.NewMessage(ws.MsgTypeSyncRequest, req)
	if err != nil {
		return err
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	return d.writeMessage(data)
}

// readLocalTimestamp 读取本地指定域名的时间戳
func (d *Daemon) readLocalTimestamp(workDir, domain string) int64 {
	content, err := os.ReadFile(filepath.Join(workDir, domain, "time.log"))
	if err != nil {
		return 0 // 文件不存在返回 0，表示需要同步
	}
	return cert.ParseTimeLog(content)
}

// collectAllLocalTimestamps 收集本地所有域名的时间戳（用于全局订阅 "*"）
func (d *Daemon) collectAllLocalTimestamps(workDir string, timestamps map[string]int64) {
	entries, err := os.ReadDir(workDir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		domain := entry.Name()
		ts := d.readLocalTimestamp(workDir, domain)
		timestamps[domain] = ts
	}
}

// syncLoop 定时同步循环
func (d *Daemon) syncLoop(ctx context.Context) {
	d.mu.RLock()
	interval := d.config.SyncInterval
	d.mu.RUnlock()

	if interval <= 0 {
		slog.Debug("定时同步已禁用")
		return
	}

	slog.Info("启动定时同步", "interval", interval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			slog.Debug("执行定时证书同步")
			if err := d.requestSync(); err != nil {
				slog.Warn("定时同步请求失败", "error", err)
			}
		}
	}
}
