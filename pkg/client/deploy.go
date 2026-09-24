package client

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/Catker/acmeDeliver/pkg/cert"
	"github.com/Catker/acmeDeliver/pkg/config"
)

// certFiles 保存到工作目录/部署到站点的证书文件（time.log 单独在最后写入）
var certFiles = []string{"cert.pem", "key.pem", "fullchain.pem"}

// ApplyCert 应用一次证书更新（CLI 与 Daemon 共用），顺序固定：
// 1. cert/key/fullchain 保存到 <workDir>/<domain>/（空内容跳过，其余文件名忽略）
// 2. 部署到站点路径（site 为 nil 时跳过）
// 3. 最后写 time.log：部署失败时不写，避免下次同步被误判为已是最新
func ApplyCert(workDir, domain string, files map[string][]byte, site *config.SiteDeployConfig) error {
	domainDir, err := cert.SafeDomainDir(workDir, domain)
	if err != nil {
		return err
	}

	for _, name := range certFiles {
		if len(files[name]) == 0 {
			continue
		}
		if err := cert.WriteFileAtomic(filepath.Join(domainDir, name), files[name], cert.CertFilePerm(name)); err != nil {
			return fmt.Errorf("保存 %s 到工作目录失败: %w", name, err)
		}
	}
	slog.Info("证书已保存到工作目录", "dir", domainDir)

	if site != nil {
		if err := DeploySite(site, domain, files); err != nil {
			return fmt.Errorf("部署证书失败: %w", err)
		}
		slog.Info("证书部署完成", "domain", domain)
	}

	if timeLog := files["time.log"]; len(timeLog) > 0 {
		if err := cert.WriteFileAtomic(filepath.Join(domainDir, "time.log"), timeLog, cert.PermCert); err != nil {
			return fmt.Errorf("保存 time.log 失败: %w", err)
		}
	}
	return nil
}

// DeploySite 将证书写入站点配置的目标路径（路径中的 {domain} 替换为实际域名，未配置的路径跳过）。
// 先整体校验：任一配置目标的源内容缺失或为空即拒绝，不做部分写入；再逐个原子写入。
// 只写文件，reload 由调用方统一执行。
func DeploySite(site *config.SiteDeployConfig, domain string, files map[string][]byte) error {
	targets := map[string]string{
		"cert.pem":      site.CertPath,
		"key.pem":       site.KeyPath,
		"fullchain.pem": site.FullchainPath,
	}
	for _, name := range certFiles {
		if targets[name] != "" && len(files[name]) == 0 {
			return fmt.Errorf("%s 内容为空，拒绝部署", name)
		}
	}
	for _, name := range certFiles {
		if targets[name] == "" {
			continue
		}
		dst := strings.ReplaceAll(targets[name], "{domain}", domain)
		if err := cert.WriteFileAtomic(dst, files[name], cert.CertFilePerm(name)); err != nil {
			return fmt.Errorf("写入 %s 失败: %w", dst, err)
		}
		slog.Info("证书文件已部署", "file", name, "path", dst)
	}
	return nil
}

// ReadLocalTimestamp 读取工作目录 <workDir>/<domain>/time.log 中的时间戳，不存在或无效返回 0
func ReadLocalTimestamp(workDir, domain string) int64 {
	content, err := os.ReadFile(filepath.Join(workDir, domain, "time.log"))
	if err != nil {
		return 0
	}
	return cert.ParseTimeLog(content)
}
