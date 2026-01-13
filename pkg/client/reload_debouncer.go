// Package client 提供客户端功能，包括 daemon 模式
package client

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/Catker/acmeDeliver/pkg/command"
)

// ReloadDebouncer 实现 reload 命令的防抖功能
// 用于 Daemon 模式，避免短时间内多个证书更新时重复执行 reload
type ReloadDebouncer struct {
	mu          sync.Mutex
	timer       *time.Timer
	delay       time.Duration
	pendingCmds map[string]struct{} // 待执行的 reload 命令（去重）
	executing   bool
}

// NewReloadDebouncer 创建新的防抖器
func NewReloadDebouncer(delay time.Duration) *ReloadDebouncer {
	return &ReloadDebouncer{
		delay:       delay,
		pendingCmds: make(map[string]struct{}),
	}
}

// Trigger 触发 reload 请求（防抖）
// 每次调用会重置计时器，直到静默期过后才真正执行
func (r *ReloadDebouncer) Trigger(reloadCmd string) {
	if reloadCmd == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// 添加到待执行队列（去重）
	r.pendingCmds[reloadCmd] = struct{}{}

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

// execute 实际执行 reload（内部方法，由计时器触发）
func (r *ReloadDebouncer) execute() {
	r.mu.Lock()
	if r.executing || len(r.pendingCmds) == 0 {
		r.mu.Unlock()
		return
	}
	r.executing = true

	// 复制待执行命令并清空队列
	cmds := make([]string, 0, len(r.pendingCmds))
	for cmd := range r.pendingCmds {
		cmds = append(cmds, cmd)
	}
	r.pendingCmds = make(map[string]struct{})
	r.mu.Unlock()

	// 执行所有 reload 命令（去重后）
	slog.Info("开始执行防抖后的重载命令", "count", len(cmds))
	for _, cmd := range cmds {
		r.executeCmd(cmd)
	}

	r.mu.Lock()
	r.executing = false
	r.mu.Unlock()
}

// executeCmd 执行单个 reload 命令
func (r *ReloadDebouncer) executeCmd(cmd string) {
	slog.Info("执行重载命令", "cmd", cmd)
	if err := command.ExecuteWithStdio(context.Background(), cmd, 15*time.Second); err != nil {
		slog.Error("重载命令执行失败", "cmd", cmd, "error", err)
	} else {
		slog.Info("重载命令执行成功", "cmd", cmd)
	}
}
