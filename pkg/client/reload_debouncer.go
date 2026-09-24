// Package client 提供客户端功能，包括 daemon 模式
package client

import (
	"log/slog"
	"sync"
	"time"

	"github.com/Catker/acmeDeliver/pkg/command"
)

// reload 失败后的退避重试参数：30s、60s、120s、240s、480s，共约 15 分钟
// time.log 在 reload 之前已写入，失败若不重试，服务将一直使用旧证书直到下次续期
const (
	defaultReloadRetryBackoff = 30 * time.Second
	defaultReloadMaxRetries   = 5
)

// ReloadDebouncer 实现 reload 命令的防抖功能
// 用于 Daemon 模式，避免短时间内多个证书更新时重复执行 reload；
// 执行失败的命令按指数退避有限次重试
type ReloadDebouncer struct {
	mu           sync.Mutex
	timer        *time.Timer
	delay        time.Duration
	pendingCmds  map[string]int         // 待执行的 reload 命令（去重）-> 已重试次数
	retryTimers  map[string]*time.Timer // 等待退避重试的命令
	stopped      bool                   // Flush 后不再安排重试
	retryBackoff time.Duration          // 首次重试间隔，之后每次翻倍
	maxRetries   int
	execMu       sync.Mutex // 串行化执行；执行期间触发的计时器会等待而非丢弃
}

// NewReloadDebouncer 创建新的防抖器
func NewReloadDebouncer(delay time.Duration) *ReloadDebouncer {
	return &ReloadDebouncer{
		delay:        delay,
		pendingCmds:  make(map[string]int),
		retryTimers:  make(map[string]*time.Timer),
		retryBackoff: defaultReloadRetryBackoff,
		maxRetries:   defaultReloadMaxRetries,
	}
}

// Trigger 触发 reload 请求（防抖）
// 每次调用会重置计时器，直到静默期过后才真正执行；
// 若该命令正在等待重试，取消重试并按新请求重新计数
func (r *ReloadDebouncer) Trigger(reloadCmd string) {
	if reloadCmd == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if t, ok := r.retryTimers[reloadCmd]; ok {
		t.Stop()
		delete(r.retryTimers, reloadCmd)
	}

	// 添加到待执行队列（去重），重置重试计数
	r.pendingCmds[reloadCmd] = 0

	// 重置计时器
	if r.timer != nil {
		r.timer.Stop()
	}

	r.timer = time.AfterFunc(r.delay, r.execute)

	slog.Debug("Reload 已加入队列，等待防抖",
		"cmd", reloadCmd,
		"delay", r.delay,
		"pending_count", len(r.pendingCmds))
}

// Flush 停止所有计时器并立即同步执行当前待执行的命令（含等待重试中的），
// 此后不再安排重试（用于退出前不丢失防抖中的 reload）
func (r *ReloadDebouncer) Flush() {
	r.mu.Lock()
	r.stopped = true
	if r.timer != nil {
		r.timer.Stop()
	}
	for cmd, t := range r.retryTimers {
		t.Stop()
		if _, ok := r.pendingCmds[cmd]; !ok {
			r.pendingCmds[cmd] = 0
		}
	}
	r.retryTimers = make(map[string]*time.Timer)
	r.mu.Unlock()
	r.execute()
}

// execute 实际执行 reload（内部方法，由计时器触发）
// 通过 execMu 串行执行：执行期间新 Trigger 的命令留在 pending 中，
// 由其计时器触发的下一次 execute 在当前执行结束后取走执行
func (r *ReloadDebouncer) execute() {
	r.execMu.Lock()
	defer r.execMu.Unlock()

	// 取走当前全部待执行命令并清空队列
	r.mu.Lock()
	cmds := r.pendingCmds
	r.pendingCmds = make(map[string]int)
	r.mu.Unlock()

	if len(cmds) == 0 {
		return
	}

	// 执行所有 reload 命令（去重后）
	slog.Info("开始执行防抖后的重载命令", "count", len(cmds))
	for cmd, retries := range cmds {
		if !r.executeCmd(cmd) {
			r.scheduleRetry(cmd, retries)
		}
	}
}

// scheduleRetry 为失败的命令安排退避重试（retries 为该命令已重试次数）
func (r *ReloadDebouncer) scheduleRetry(cmd string, retries int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// 执行期间已被新 Trigger 重新排队，交由正常防抖执行
	if _, ok := r.pendingCmds[cmd]; ok {
		return
	}
	if r.stopped {
		slog.Error("重载命令失败且客户端正在退出，需人工处理", "cmd", cmd)
		return
	}
	if retries >= r.maxRetries {
		slog.Error("重载命令重试耗尽，服务可能仍在使用旧证书，需人工处理",
			"cmd", cmd, "retries", retries)
		return
	}

	backoff := r.retryBackoff << retries
	var t *time.Timer
	t = time.AfterFunc(backoff, func() {
		r.mu.Lock()
		// 已被 Trigger/Flush 取消或替换
		if r.retryTimers[cmd] != t {
			r.mu.Unlock()
			return
		}
		delete(r.retryTimers, cmd)
		r.mu.Unlock()
		r.retry(cmd, retries+1)
	})
	r.retryTimers[cmd] = t

	slog.Warn("重载命令将退避重试", "cmd", cmd, "attempt", retries+1, "backoff", backoff)
}

// retry 串行执行单个重试命令（retries 为包含本次在内的已重试次数）
func (r *ReloadDebouncer) retry(cmd string, retries int) {
	r.execMu.Lock()
	defer r.execMu.Unlock()

	// 等待执行锁期间被新 Trigger 重新排队，交由正常防抖执行
	r.mu.Lock()
	_, pending := r.pendingCmds[cmd]
	r.mu.Unlock()
	if pending {
		return
	}

	if !r.executeCmd(cmd) {
		r.scheduleRetry(cmd, retries)
	}
}

// executeCmd 执行单个 reload 命令，返回是否成功
func (r *ReloadDebouncer) executeCmd(cmd string) bool {
	slog.Info("执行重载命令", "cmd", cmd)
	if err := command.Execute(cmd, 15*time.Second); err != nil {
		slog.Error("重载命令执行失败", "cmd", cmd, "error", err)
		return false
	}
	slog.Info("重载命令执行成功", "cmd", cmd)
	return true
}
