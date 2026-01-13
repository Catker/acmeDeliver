// Package cert 提供证书解析和状态收集的公共工具函数
package cert

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DomainStatus 表示域名证书的完整状态信息
type DomainStatus struct {
	Domain        string `json:"domain"`                   // 域名
	LastUpdate    int64  `json:"last_update,omitempty"`    // 最后更新时间（Unix 时间戳）
	HasCert       bool   `json:"has_cert"`                 // 是否有 cert.pem
	HasKey        bool   `json:"has_key"`                  // 是否有 key.pem
	HasFullchain  bool   `json:"has_fullchain"`            // 是否有 fullchain.pem
	CertSize      int64  `json:"cert_size,omitempty"`      // cert.pem 大小
	KeySize       int64  `json:"key_size,omitempty"`       // key.pem 大小
	FullchainSize int64  `json:"fullchain_size,omitempty"` // fullchain.pem 大小
	Valid         bool   `json:"valid"`                    // 整体有效性
	NotBefore     int64  `json:"not_before,omitempty"`     // 证书生效时间
	NotAfter      int64  `json:"not_after,omitempty"`      // 证书过期时间
	DaysRemaining int    `json:"days_remaining,omitempty"` // 剩余有效天数
	Subject       string `json:"subject,omitempty"`        // 证书主题
	Issuer        string `json:"issuer,omitempty"`         // 颁发者
	Error         string `json:"error,omitempty"`          // 错误信息
}

// ParseCertificate 解析 PEM 格式的证书文件
// 返回第一个有效证书的信息
func ParseCertificate(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, fmt.Errorf("无效的 PEM 数据")
	}
	if block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("不是证书类型: %s", block.Type)
	}
	return x509.ParseCertificate(block.Bytes)
}

// CollectDomainStatus 收集单个域名的证书状态
func CollectDomainStatus(baseDir, domain string) DomainStatus {
	domainDir := filepath.Join(baseDir, domain)
	status := DomainStatus{Domain: domain}

	// 检查 time.log
	timeLogPath := filepath.Join(domainDir, "time.log")
	if content, err := os.ReadFile(timeLogPath); err == nil {
		ts := strings.TrimSpace(string(content))
		if len(ts) >= 10 {
			ts = ts[:10] // 只取前10位
		}
		if t, err := strconv.ParseInt(ts, 10, 64); err == nil {
			status.LastUpdate = t
		}
	}

	// 检查 cert.pem
	certPath := filepath.Join(domainDir, "cert.pem")
	if info, err := os.Stat(certPath); err == nil {
		status.HasCert = true
		status.CertSize = info.Size()

		// 解析证书有效期
		if status.CertSize > 0 {
			if certData, err := os.ReadFile(certPath); err == nil {
				if cert, err := ParseCertificate(certData); err == nil {
					status.NotBefore = cert.NotBefore.Unix()
					status.NotAfter = cert.NotAfter.Unix()
					status.DaysRemaining = int(time.Until(cert.NotAfter).Hours() / 24)
					status.Subject = cert.Subject.CommonName
					// 获取颁发者信息
					if cert.Issuer.CommonName != "" {
						status.Issuer = cert.Issuer.CommonName
					} else if len(cert.Issuer.Organization) > 0 {
						status.Issuer = cert.Issuer.Organization[0]
					}
				}
			}
		}
	}

	// 检查 key.pem
	keyPath := filepath.Join(domainDir, "key.pem")
	if info, err := os.Stat(keyPath); err == nil {
		status.HasKey = true
		status.KeySize = info.Size()
	}

	// 检查 fullchain.pem
	fullchainPath := filepath.Join(domainDir, "fullchain.pem")
	if info, err := os.Stat(fullchainPath); err == nil {
		status.HasFullchain = true
		status.FullchainSize = info.Size()
	}

	// 判定整体有效性：三个文件都存在且非空
	status.Valid = status.HasCert && status.HasKey && status.HasFullchain &&
		status.CertSize > 0 && status.KeySize > 0 && status.FullchainSize > 0

	// 设置错误信息
	if !status.Valid {
		if !status.HasCert || !status.HasKey || !status.HasFullchain {
			status.Error = "缺少必需文件"
		} else if status.CertSize == 0 || status.KeySize == 0 || status.FullchainSize == 0 {
			status.Error = "文件为空"
		}
	}

	return status
}

// CollectAllDomainStatus 收集目录下所有域名的证书状态
func CollectAllDomainStatus(baseDir string) []DomainStatus {
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return nil
	}

	var domains []DomainStatus
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		domains = append(domains, CollectDomainStatus(baseDir, entry.Name()))
	}
	return domains
}
