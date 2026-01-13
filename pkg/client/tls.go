// Package client 提供客户端 TLS 配置功能
package client

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"os"
)

// TLSConfig 客户端 TLS 配置
type TLSConfig struct {
	CaFile             string // CA 证书路径（用于验证服务端身份）
	InsecureSkipVerify bool   // 跳过证书验证（仅开发环境使用）
}

// BuildTLSConfig 构建 TLS 配置
// 返回值：
//   - nil, nil: 使用系统默认配置
//   - *tls.Config, nil: 使用自定义配置
//   - nil, error: 配置错误
func BuildTLSConfig(cfg *TLSConfig) (*tls.Config, error) {
	if cfg == nil {
		return nil, nil
	}

	// 无自定义配置时返回 nil，使用系统默认
	if cfg.CaFile == "" && !cfg.InsecureSkipVerify {
		return nil, nil
	}

	tlsConfig := &tls.Config{
		InsecureSkipVerify: cfg.InsecureSkipVerify,
	}

	if cfg.InsecureSkipVerify {
		slog.Warn("⚠️ TLS 证书验证已禁用，仅用于开发环境")
	}

	// 加载自定义 CA 证书
	if cfg.CaFile != "" {
		caCert, err := os.ReadFile(cfg.CaFile)
		if err != nil {
			return nil, fmt.Errorf("加载 CA 证书失败: %w", err)
		}

		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("解析 CA 证书失败: 无效的 PEM 格式")
		}

		tlsConfig.RootCAs = caCertPool
		slog.Info("🔒 已加载自定义 CA 证书", "file", cfg.CaFile)
	}

	return tlsConfig, nil
}
