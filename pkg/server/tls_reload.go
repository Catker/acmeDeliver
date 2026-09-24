package server

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"
)

// certReloader 按证书/私钥文件 mtime 变化惰性重新加载 TLS 证书，
// 使续期后的证书无需重启服务即可生效
type certReloader struct {
	certFile, keyFile string

	mu        sync.Mutex
	cert      *tls.Certificate
	certMtime time.Time
	keyMtime  time.Time
}

// newCertReloader 创建并立即加载一次证书；加载失败返回错误
func newCertReloader(certFile, keyFile string) (*certReloader, error) {
	r := &certReloader{certFile: certFile, keyFile: keyFile}
	certMtime, keyMtime, err := r.mtimes()
	if err != nil {
		return nil, err
	}
	if err := r.load(certMtime, keyMtime); err != nil {
		return nil, err
	}
	return r, nil
}

// mtimes 读取证书与私钥文件的修改时间
func (r *certReloader) mtimes() (time.Time, time.Time, error) {
	certInfo, err := os.Stat(r.certFile)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("读取证书文件信息失败: %w", err)
	}
	keyInfo, err := os.Stat(r.keyFile)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("读取私钥文件信息失败: %w", err)
	}
	return certInfo.ModTime(), keyInfo.ModTime(), nil
}

// load 加载证书并记录对应 mtime（调用方需持有锁或处于初始化阶段）
func (r *certReloader) load(certMtime, keyMtime time.Time) error {
	c, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return fmt.Errorf("加载 TLS 证书失败: %w", err)
	}
	r.cert = &c
	r.certMtime = certMtime
	r.keyMtime = keyMtime
	return nil
}

// GetCertificate 供 tls.Config 使用：文件 mtime 变化时重新加载，失败时继续使用旧证书
func (r *certReloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	certMtime, keyMtime, err := r.mtimes()
	if err != nil {
		slog.Warn("⚠️ 检查 TLS 证书失败，继续使用旧证书", "error", err)
		return r.cert, nil
	}
	if certMtime.Equal(r.certMtime) && keyMtime.Equal(r.keyMtime) {
		return r.cert, nil
	}
	if err := r.load(certMtime, keyMtime); err != nil {
		slog.Warn("⚠️ 重新加载 TLS 证书失败，继续使用旧证书", "error", err)
		return r.cert, nil
	}
	slog.Info("🔄 TLS 证书已重新加载", "cert", r.certFile)
	return r.cert, nil
}
