// Package server 提供服务端功能
package server

import (
	"context"
	"fmt"
	"log/slog"
)

// namedCloser 带名称的关闭函数
type namedCloser struct {
	name   string
	closer func(ctx context.Context) error
}

// GracefulShutdown 优雅关闭管理器
// 按添加顺序依次关闭所有组件；单项失败记录错误后继续关闭其余组件。
// Add 与 Shutdown 均在启动/停止单 goroutine 中调用，不额外加锁。
type GracefulShutdown struct {
	components []namedCloser
}

// NewGracefulShutdown 创建优雅关闭管理器
func NewGracefulShutdown() *GracefulShutdown {
	return &GracefulShutdown{}
}

// AddFunc 添加命名关闭函数，组件将按添加顺序依次关闭
func (g *GracefulShutdown) AddFunc(name string, fn func(ctx context.Context) error) {
	g.components = append(g.components, namedCloser{name: name, closer: fn})
}

// Shutdown 执行优雅关闭，返回按组件命名的错误列表（可能为空）
func (g *GracefulShutdown) Shutdown(ctx context.Context) []error {
	var errors []error

	for _, nc := range g.components {
		slog.Info("⏳ 正在关闭...", "component", nc.name)

		if err := nc.closer(ctx); err != nil {
			slog.Warn("⚠️ 关闭错误", "component", nc.name, "error", err)
			errors = append(errors, fmt.Errorf("%s: %w", nc.name, err))
		} else {
			slog.Info("✅ 已关闭", "component", nc.name)
		}
	}

	return errors
}
