// Package watcher 提供证书目录监控功能
// 使用 fsnotify 监控证书文件变化，当检测到更新时触发回调
package watcher

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/Catker/acmeDeliver/pkg/cert"
)

// CertWatcher 证书目录监控器
type CertWatcher struct {
	baseDir  string
	watcher  *fsnotify.Watcher
	onChange func(domain string, files map[string][]byte)
	debounce time.Duration
	stop     chan struct{} // 停止信号
}

// NewCertWatcher 创建新的证书监控器
func NewCertWatcher(baseDir string, debounce time.Duration) (*CertWatcher, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	return &CertWatcher{
		baseDir:  baseDir,
		watcher:  watcher,
		debounce: debounce,
		stop:     make(chan struct{}),
	}, nil
}

// OnChange 设置证书变更回调
func (w *CertWatcher) OnChange(callback func(domain string, files map[string][]byte)) {
	w.onChange = callback
}

// Start 开始监控
func (w *CertWatcher) Start() error {
	// 添加基础目录
	if err := w.watcher.Add(w.baseDir); err != nil {
		return err
	}

	// 添加所有现有的域名目录
	entries, err := os.ReadDir(w.baseDir)
	if err != nil {
		slog.Warn("读取证书目录失败", "dir", w.baseDir, "error", err)
	} else {
		for _, entry := range entries {
			if entry.IsDir() {
				domainPath := filepath.Join(w.baseDir, entry.Name())
				if err := w.watcher.Add(domainPath); err != nil {
					slog.Warn("添加域名目录监控失败", "dir", domainPath, "error", err)
				}
			}
		}
	}

	// 启动事件处理协程
	go w.eventLoop()

	slog.Info("证书目录监控已启动", "baseDir", w.baseDir, "debounce", w.debounce)
	return nil
}

// Stop 停止监控
func (w *CertWatcher) Stop() error {
	close(w.stop)
	return w.watcher.Close()
}

// eventLoop 事件处理循环
func (w *CertWatcher) eventLoop() {
	// 防抖处理: 收集一段时间内的事件，合并处理
	pendingDomains := make(map[string]time.Time)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-w.stop:
			return

		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			w.handleEvent(event, pendingDomains)

		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			slog.Error("监控错误", "error", err)

		case <-ticker.C:
			// 处理已经超过防抖时间的域名
			w.processPending(pendingDomains)
		}
	}
}

// handleEvent 处理单个文件事件
func (w *CertWatcher) handleEvent(event fsnotify.Event, pending map[string]time.Time) {
	// 只关心写入和创建事件
	if event.Op&(fsnotify.Write|fsnotify.Create) == 0 {
		return
	}

	path := event.Name

	// 判断是否是域名目录下的文件
	relPath, err := filepath.Rel(w.baseDir, path)
	if err != nil {
		return
	}

	parts := strings.Split(relPath, string(filepath.Separator))
	if len(parts) == 1 {
		// baseDir 下的直接子项只可能是：
		// 1. 新建域名目录：需要补挂 watcher，并触发一次目录扫描
		// 2. 普通文件：忽略
		if event.Op&fsnotify.Create == 0 {
			return
		}

		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			return
		}

		domain := parts[0]
		if err := w.watcher.Add(path); err != nil {
			slog.Warn("添加新域名目录监控失败", "dir", path, "error", err)
		}

		pending[domain] = time.Now()
		slog.Debug("检测到新域名目录", "domain", domain, "dir", path)
		return
	}

	domain := parts[0]
	pending[domain] = time.Now()
	slog.Debug("检测到证书文件变化", "domain", domain, "file", filepath.Base(path))
}

// processPending 处理待处理的域名更新
func (w *CertWatcher) processPending(pending map[string]time.Time) {
	now := time.Now()

	for domain, lastEvent := range pending {
		// 检查是否超过防抖时间
		if now.Sub(lastEvent) < w.debounce {
			continue
		}

		delete(pending, domain)

		// 读取证书文件并触发回调
		if w.onChange != nil {
			files, err := w.readCertFiles(domain)
			if err != nil {
				slog.Error("读取证书文件失败", "domain", domain, "error", err)
				continue
			}
			if len(files) > 0 {
				slog.Info("触发证书推送", "domain", domain, "files", len(files))
				w.onChange(domain, files)
			}
		}
	}
}

// readCertFiles 读取域名下需要下发的证书文件（仅 cert.DeliverFiles，缺失的跳过）
// 域名目录不存在时返回错误
func (w *CertWatcher) readCertFiles(domain string) (map[string][]byte, error) {
	domainPath := filepath.Join(w.baseDir, domain)
	if _, err := os.Stat(domainPath); err != nil {
		return nil, err
	}

	return cert.ReadDeliverFiles(domainPath), nil
}
