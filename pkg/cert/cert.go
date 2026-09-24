// Package cert 提供证书解析和状态收集的公共工具函数
package cert

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// DeliverFiles 服务端下发给客户端的证书文件（watcher 推送、CLI 请求、Daemon 同步共用）
var DeliverFiles = []string{"cert.pem", "key.pem", "fullchain.pem", "time.log"}

// ReadDeliverFiles 读取域名目录下的 DeliverFiles（缺失的静默跳过，其他读取错误告警后跳过）
func ReadDeliverFiles(domainDir string) map[string][]byte {
	files := make(map[string][]byte)
	for _, name := range DeliverFiles {
		path := filepath.Join(domainDir, name)
		content, err := os.ReadFile(path)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				slog.Warn("读取下发文件失败，已跳过", "path", path, "error", err)
			}
			continue
		}
		files[name] = content
	}
	return files
}

// MatchWildcard 检查域名是否匹配通配符模式
// 支持 *.example.com 形式的通配符
func MatchWildcard(pattern, domain string) bool {
	if len(pattern) < 2 || pattern[0] != '*' || pattern[1] != '.' {
		return false
	}

	suffix := pattern[1:] // .example.com
	if len(domain) <= len(suffix) {
		return false
	}

	// 检查域名是否以 .example.com 结尾
	return domain[len(domain)-len(suffix):] == suffix
}

// 证书文件写入权限约定（CLI 与 Daemon 共用）
const (
	// PermCert 普通证书文件权限
	PermCert os.FileMode = 0644
	// PermKey 私钥文件权限（仅所有者可读写）
	PermKey os.FileMode = 0600
)

// CertFilePerm 按文件名返回写入权限：私钥（key.pem / *.key）0600，其余 0644
func CertFilePerm(filename string) os.FileMode {
	if filename == "key.pem" || strings.HasSuffix(filename, ".key") {
		return PermKey
	}
	return PermCert
}

// SafeDomainDir 校验域名（非空、不含 /、\、..）并返回 baseDir 下的域名目录
func SafeDomainDir(baseDir, domain string) (string, error) {
	if domain == "" || strings.ContainsAny(domain, `/\`) || strings.Contains(domain, "..") {
		return "", fmt.Errorf("非法域名: %q", domain)
	}
	return filepath.Join(baseDir, domain), nil
}

// WriteFileAtomic 以原子方式写入文件：先写同目录唯一临时文件，再重命名替换目标文件。
// 临时文件名带随机后缀，不会误删或覆盖其他写入者/用户的同名 .tmp 文件；
// 失败时只清理本次创建的临时文件。
//   - followSymlink 为 true 且目标是软链接时写入其指向的真实文件（在真实文件所在目录替换），
//     软链接本身保留；悬空软链接返回错误；
//   - followSymlink 为 false 时不跟随：目标是软链接则由 rename 直接替换软链接本身为普通文件，
//     不写入链接目标，也不沿用链接目标的权限/属主（用于工作目录，防止他人预置软链接）；
//   - 目标是已存在的普通文件时沿用其权限位（忽略 perm），并尽量沿用原属主/属组（chown 失败仅记 Debug）；
//   - 其他情况使用 perm。
func WriteFileAtomic(path string, content []byte, perm os.FileMode, followSymlink bool) error {
	if path == "" {
		return fmt.Errorf("文件路径不能为空")
	}

	if followSymlink {
		resolved, err := resolveSymlink(path)
		if err != nil {
			return err
		}
		path = resolved
	}

	// 已存在的普通文件：沿用其权限与属主（Lstat 不跟随软链接）
	var existing os.FileInfo
	if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() {
		existing = info
		perm = info.Mode().Perm()
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}

	// 同目录内唯一临时文件（随机后缀），避免与其他并发写入者冲突
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	tempPath := tmp.Name()

	replaced := false
	// 仅在替换未成功时清理自己创建的临时文件
	defer func() {
		if !replaced {
			os.Remove(tempPath)
		}
	}()

	// 先 chown 再 chmod（chown 可能清除特殊权限位）
	if existing != nil {
		if st, ok := existing.Sys().(*syscall.Stat_t); ok {
			if err := tmp.Chown(int(st.Uid), int(st.Gid)); err != nil {
				slog.Debug("沿用原文件属主失败，使用当前用户", "path", path, "error", err)
			}
		}
	}
	// CreateTemp 固定 0600，按目标权限显式设置（不受 umask 影响）
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return fmt.Errorf("设置临时文件权限失败: %w", err)
	}
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return fmt.Errorf("写入临时文件失败: %w", err)
	}
	// rename 前落盘：否则断电后可能得到空文件，而 time.log 已写入、不会再推送重试
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("同步临时文件失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时文件失败: %w", err)
	}

	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("替换文件失败: %w", err)
	}
	replaced = true

	return nil
}

// resolveSymlink 若 path 是软链接则返回其真实目标路径（悬空软链接返回错误）；否则原样返回
func resolveSymlink(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return path, nil
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("软链接 %s 的目标不存在或无法解析: %w", path, err)
	}
	return real, nil
}

// ParseTimeLog 解析 time.log 内容为 Unix 时间戳（秒）。
// 统一规则：先 TrimSpace，再截取前 10 位；解析失败返回 0。
func ParseTimeLog(content []byte) int64 {
	ts := strings.TrimSpace(string(content))
	if len(ts) > 10 {
		ts = ts[:10]
	}
	t, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return 0
	}
	return t
}

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
		status.LastUpdate = ParseTimeLog(content)
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
