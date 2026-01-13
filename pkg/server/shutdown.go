// Package server 提供服务端功能
package server

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
)

// Shutdownable 可关闭组件接口
// 任何需要优雅关闭的组件都应实现此接口
type Shutdownable interface {
	Shutdown(ctx context.Context) error
}

// ShutdownFunc 函数适配器，将函数转换为 Shutdownable 接口
type ShutdownFunc func(ctx context.Context) error

// Shutdown 实现 Shutdownable 接口
func (f ShutdownFunc) Shutdown(ctx context.Context) error {
	return f(ctx)
}

// NamedComponent 带名称的可关闭组件
type NamedComponent struct {
	Name      string
	Component Shutdownable
}

// GracefulShutdown 优雅关闭管理器
// 管理多个组件的有序关闭，确保资源按添加顺序依次释放
type GracefulShutdown struct {
	mu         sync.Mutex
	components []NamedComponent
	logger     *slog.Logger
}

// NewGracefulShutdown 创建优雅关闭管理器
func NewGracefulShutdown() *GracefulShutdown {
	return &GracefulShutdown{
		components: make([]NamedComponent, 0),
		logger:     slog.Default(),
	}
}

// Add 添加可关闭组件
// 组件将按添加顺序依次关闭
func (g *GracefulShutdown) Add(name string, component Shutdownable) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.components = append(g.components, NamedComponent{
		Name:      name,
		Component: component,
	})
}

// AddFunc 添加关闭函数
// 便捷方法，将函数包装为 Shutdownable
func (g *GracefulShutdown) AddFunc(name string, fn func(ctx context.Context) error) {
	g.Add(name, ShutdownFunc(fn))
}

// Shutdown 执行优雅关闭
// 按添加顺序依次关闭所有组件，收集所有错误
// 即使某个组件关闭失败，也会继续关闭其他组件
func (g *GracefulShutdown) Shutdown(ctx context.Context) []error {
	g.mu.Lock()
	components := make([]NamedComponent, len(g.components))
	copy(components, g.components)
	logger := g.logger
	g.mu.Unlock()

	var errors []error

	for _, nc := range components {
		if logger != nil {
			logger.Info("⏳ 正在关闭...", "component", nc.Name)
		}

		if err := nc.Component.Shutdown(ctx); err != nil {
			if logger != nil {
				logger.Warn("⚠️ 关闭错误", "component", nc.Name, "error", err)
			}
			errors = append(errors, fmt.Errorf("%s: %w", nc.Name, err))
		} else {
			if logger != nil {
				logger.Info("✅ 已关闭", "component", nc.Name)
			}
		}
	}

	return errors
}
