// Package client 提供客户端功能，包括 daemon 模式
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Catker/acmeDeliver/pkg/config"
	"github.com/Catker/acmeDeliver/pkg/security"
	ws "github.com/Catker/acmeDeliver/pkg/websocket"
)

// readTimeout 读超时：服务端每 45s 发送 WebSocket ping，收到 ping 即续期；
// 超过该时间未收到 ping 视为连接失效，断开后退避重连
const readTimeout = 3 * time.Minute

// DaemonConfig Daemon 模式配置
type DaemonConfig struct {
	ServerURL         string                    // WebSocket 服务器地址
	Password          string                    // 认证密码
	ClientID          string                    // 客户端标识
	WorkDir           string                    // 工作目录
	Subscribe         []string                  // 订阅的域名列表
	Sites             []config.SiteDeployConfig // 站点部署配置
	ReconnectInterval time.Duration             // 重连间隔
	ReloadDebounce    time.Duration             // Reload 防抖延迟（默认 5 秒）
	SyncInterval      time.Duration             // 定时同步间隔（<=0 禁用）
	DefaultReloadCmd  string                    // 站点未配置 reloadcmd 时使用的默认重载命令
	TLSConfig         *TLSConfig                // TLS 配置（可选）
}

// Daemon 客户端守护进程
type Daemon struct {
	config *DaemonConfig
	mu     sync.RWMutex // 保护 config.Subscribe / config.Sites（热重载）
	conn   *websocket.Conn
	connMu sync.Mutex // 保护 conn 的替换与写入

	reloadDebouncer *ReloadDebouncer

	// 本次连接是否已认证成功（仅在 Run 所在协程读写：connectAndServe/readLoop/handleMessage）
	connAuthed bool
}

// NewDaemon 创建新的 Daemon
func NewDaemon(cfg *DaemonConfig) *Daemon {
	if cfg.ReloadDebounce <= 0 {
		cfg.ReloadDebounce = 5 * time.Second
	}
	return &Daemon{
		config:          cfg,
		reloadDebouncer: NewReloadDebouncer(cfg.ReloadDebounce),
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

// writeMessage 序列化并线程安全地写入当前连接；尚未建立连接时跳过
func (d *Daemon) writeMessage(msg *ws.Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	d.connMu.Lock()
	defer d.connMu.Unlock()
	if d.conn == nil {
		return nil
	}
	return d.conn.WriteMessage(websocket.TextMessage, data)
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
		if err := d.connectAndServe(ctx); err != nil && ctx.Err() == nil {
			slog.Error("连接断开", "error", err)
		}
		if ctx.Err() != nil {
			slog.Info("收到退出信号，正在退出")
			return nil
		}
		// 本次连接曾认证成功即视为连上过，重置退避计数（readLoop 断开时总返回错误）
		if d.connAuthed {
			attempt = 0
		}

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

// connectAndServe 连接服务器并处理消息，连接断开时返回
func (d *Daemon) connectAndServe(ctx context.Context) error {
	d.connAuthed = false
	conn, err := dial(ctx, d.config.ServerURL, d.config.TLSConfig)
	if err != nil {
		return err
	}
	d.connMu.Lock()
	d.conn = conn
	d.connMu.Unlock()

	// 连接级 context：连接结束时取消，确保本连接的后台 goroutine 全部退出，
	// 且在下次重连替换 d.conn 之前退出完毕，避免泄漏与跨连接混用
	// defer 逆序执行：先 cancel（触发 AfterFunc 关闭连接），再关闭连接，最后等待 goroutine 退出
	var wg sync.WaitGroup
	defer wg.Wait()
	defer conn.Close()
	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// 退出信号到来时关闭连接，解除 ReadMessage 阻塞
	context.AfterFunc(connCtx, func() { conn.Close() })

	// 依赖 WebSocket 控制帧保活：收到服务端 ping 时续期读超时并回复 pong
	// （自定义 PingHandler 会替换默认的自动回复 pong；WriteControl 可与其他写并发调用）
	conn.SetReadDeadline(time.Now().Add(readTimeout))
	conn.SetPingHandler(func(appData string) error {
		conn.SetReadDeadline(time.Now().Add(readTimeout))
		// 回复失败说明连接已不可用，由后续读取返回错误触发重连
		_ = conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(10*time.Second))
		return nil
	})

	slog.Info("已连接到服务器")

	if err := d.authenticate(); err != nil {
		return err
	}

	wg.Add(1)
	go func() { defer wg.Done(); d.syncLoop(connCtx) }()

	return d.readLoop(conn)
}

// authenticate 发送认证请求（结果由 handleMessage 处理）
func (d *Daemon) authenticate() error {
	d.mu.RLock()
	subscribe := d.config.Subscribe
	d.mu.RUnlock()

	timestamp := time.Now().Unix()
	msg, err := ws.NewMessage(ws.MsgTypeAuth, &ws.AuthRequest{
		ClientID:  d.config.ClientID,
		Signature: security.NewSignatureVerifier(d.config.Password).GenerateSignature(timestamp),
		Domains:   subscribe,
	})
	if err != nil {
		return err
	}
	msg.Timestamp = timestamp

	if err := d.writeMessage(msg); err != nil {
		return err
	}
	slog.Debug("已发送认证请求", "client_id", d.config.ClientID, "domains", subscribe)
	return nil
}

// readLoop 消息读取循环，读取出错（含连接被关闭、读超时）时返回
func (d *Daemon) readLoop(conn *websocket.Conn) error {
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}

		var msg ws.Message
		if err := json.Unmarshal(data, &msg); err != nil {
			slog.Warn("无效的消息格式", "error", err)
			continue
		}
		if err := d.handleMessage(&msg); err != nil {
			return err
		}
	}
}

// handleMessage 处理收到的消息
// 返回错误表示需要断开当前连接（如认证失败），由 Run 退避重连
func (d *Daemon) handleMessage(msg *ws.Message) error {
	switch msg.Type {
	case ws.MsgTypeAuthResult:
		var resp ws.AuthResponse
		if err := msg.ParseData(&resp); err == nil {
			if !resp.Success {
				return fmt.Errorf("认证失败: %s", resp.Message)
			}
			slog.Info("认证成功", "message", resp.Message)
			d.connAuthed = true
			// 认证成功后立即请求同步证书
			if err := d.requestSync(); err != nil {
				slog.Warn("发送证书同步请求失败", "error", err)
			}
		}

	case ws.MsgTypeCertPush:
		var certData ws.CertPushData
		if err := msg.ParseData(&certData); err != nil {
			slog.Error("解析证书数据失败", "error", err)
			return nil
		}
		d.handleCertPush(&certData)

	case ws.MsgTypeError:
		var errData ws.ErrorData
		if err := msg.ParseData(&errData); err == nil {
			slog.Error("收到错误", "code", errData.Code, "message", errData.Message)
		}
	}
	return nil
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

// receiveCert 按 ApplyCert 统一顺序保存、部署证书并最后写 time.log。
// 成功时返回待防抖执行的 reload 命令（站点 reloadcmd 优先，否则 default_reload_cmd；无站点配置时为空）；
// 失败返回错误，调用方不得发送成功 ACK 或触发 reload。
func (d *Daemon) receiveCert(data *ws.CertPushData) (string, error) {
	d.mu.RLock()
	site := config.FindSiteConfig(d.config.Sites, data.Domain)
	defaultReloadCmd := d.config.DefaultReloadCmd
	d.mu.RUnlock()

	if err := ApplyCert(d.config.WorkDir, data.Domain, data.Files, site); err != nil {
		return "", err
	}
	if site == nil {
		slog.Info("未找到站点配置，跳过自动部署", "domain", data.Domain)
		return "", nil
	}
	if site.ReloadCmd != "" {
		return site.ReloadCmd, nil
	}
	return defaultReloadCmd, nil
}

// sendCertAck 发送证书接收确认
func (d *Daemon) sendCertAck(domain string, success bool, message string) {
	msg, err := ws.NewMessage(ws.MsgTypeCertAck, &ws.CertAck{
		Domain:  domain,
		Success: success,
		Message: message,
	})
	if err != nil {
		return
	}
	d.writeMessage(msg)
}

// UpdateConfig 热更新订阅与站点配置；订阅变化时向服务端发送新订阅并立即同步
func (d *Daemon) UpdateConfig(newSubscribe []string, newSites []config.SiteDeployConfig) {
	d.mu.Lock()
	oldSubscribe := d.config.Subscribe
	d.config.Subscribe = newSubscribe
	d.config.Sites = newSites
	d.mu.Unlock()

	slog.Info("应用配置更新",
		"old_subscribe", oldSubscribe,
		"new_subscribe", newSubscribe,
		"sites_count", len(newSites))

	if slices.Equal(oldSubscribe, newSubscribe) {
		return
	}
	msg, err := ws.NewMessage(ws.MsgTypeSubscribe, &ws.SubscribeRequest{Domains: newSubscribe})
	if err == nil {
		err = d.writeMessage(msg)
	}
	if err != nil {
		slog.Error("发送订阅更新失败", "error", err)
		return
	}
	slog.Info("订阅更新已发送", "domains", newSubscribe)
	// 立即同步，新增域名无需等待 sync_interval
	if err := d.requestSync(); err != nil {
		slog.Warn("发送证书同步请求失败", "error", err)
	}
}

// requestSync 收集本地订阅域名的时间戳发送给服务端，服务端比对后推送更新的证书
func (d *Daemon) requestSync() error {
	d.mu.RLock()
	subscribe := d.config.Subscribe
	d.mu.RUnlock()
	workDir := d.config.WorkDir

	timestamps := make(map[string]int64)
	for _, domain := range subscribe {
		if domain != "*" {
			timestamps[domain] = ReadLocalTimestamp(workDir, domain)
			continue
		}
		// 全局订阅：收集本地所有域名的时间戳
		entries, _ := os.ReadDir(workDir)
		for _, entry := range entries {
			if entry.IsDir() {
				timestamps[entry.Name()] = ReadLocalTimestamp(workDir, entry.Name())
			}
		}
	}

	slog.Debug("发送证书同步请求", "domains", len(timestamps))
	msg, err := ws.NewMessage(ws.MsgTypeSyncRequest, &ws.SyncRequest{Timestamps: timestamps})
	if err != nil {
		return err
	}
	return d.writeMessage(msg)
}

// syncLoop 定时同步循环（SyncInterval <= 0 时禁用）
func (d *Daemon) syncLoop(ctx context.Context) {
	interval := d.config.SyncInterval
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
