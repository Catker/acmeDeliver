package client

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/Catker/acmeDeliver/pkg/cert"
	"github.com/Catker/acmeDeliver/pkg/config"
)

// ReceiveOptions 一次证书接收的调用方差异
type ReceiveOptions struct {
	Force            bool   // 跳过时间戳比较（CLI -f）
	DryRun           bool   // 只判断不写盘（CLI --dry-run）
	ReloadOverride   string // 命令行指定的 reload 命令，优先于站点配置
	DefaultReloadCmd string // 站点未配置 reloadcmd 时的默认命令
}

// ReceiveCert 判断并落盘一次证书更新（CLI 与 Daemon 共用）：
//  1. 非 Force 时，本地工作目录 time.log 不旧于 serverTS 则跳过保存、部署与 reload，返回 ("", nil)
//  2. 校验证书与私钥配对（见 validateKeyPair），失败直接返回错误，不写任何文件
//  3. 选 reload 命令：ReloadOverride > site.ReloadCmd > DefaultReloadCmd；
//     site 为 nil 时同样使用 override/default（服务可能直接引用工作目录中的证书，仍需 reload）
//  4. DryRun 时只打日志，不写任何文件，返回 (reloadCmd, nil)
//  5. 否则调用 ApplyCert（保存 → 部署 → 最后写 time.log），site 为 nil 时仍保存到工作目录
//
// 失败时返回错误，调用方不得执行 reload。
func ReceiveCert(workDir, domain string, files map[string][]byte, serverTS int64, site *config.SiteDeployConfig, opts ReceiveOptions) (string, error) {
	if !opts.Force {
		if localTS := ReadLocalTimestamp(workDir, domain); IsCertUpToDate(localTS, serverTS) {
			slog.Info("证书未更新，跳过", "domain", domain, "local", localTS, "server", serverTS)
			return "", nil
		}
	}

	if err := validateKeyPair(files); err != nil {
		return "", fmt.Errorf("证书校验失败: %w", err)
	}

	if site == nil {
		slog.Info("未找到此域名的站点部署配置，只保存到工作目录", "domain", domain)
	}
	reloadCmd := opts.ReloadOverride
	if reloadCmd == "" && site != nil {
		reloadCmd = site.ReloadCmd
	}
	if reloadCmd == "" {
		reloadCmd = opts.DefaultReloadCmd
	}

	if opts.DryRun {
		slog.Info("[DryRun] 跳过保存与部署", "domain", domain, "site", site != nil, "cmd", reloadCmd)
		return reloadCmd, nil
	}

	if err := ApplyCert(workDir, domain, files, site); err != nil {
		return "", err
	}
	return reloadCmd, nil
}

// validateKeyPair 校验下发的证书与私钥可配对使用：key.pem 必须存在，
// cert.pem 与 fullchain.pem 至少有一个，且非空者都须与 key.pem 匹配。
// 防止服务端目录处于半更新状态（新 key 旧 cert）或文件损坏时部署出不可用的证书并 reload。
func validateKeyPair(files map[string][]byte) error {
	key := files["key.pem"]
	if len(key) == 0 {
		return fmt.Errorf("缺少 key.pem")
	}
	checked := 0
	for _, name := range []string{"cert.pem", "fullchain.pem"} {
		if len(files[name]) == 0 {
			continue
		}
		if _, err := tls.X509KeyPair(files[name], key); err != nil {
			return fmt.Errorf("%s 与 key.pem 不匹配或格式错误: %w", name, err)
		}
		checked++
	}
	if checked == 0 {
		return fmt.Errorf("缺少 cert.pem 与 fullchain.pem")
	}
	return nil
}

// certFiles 保存到工作目录/部署到站点的证书文件（time.log 单独在最后写入）
var certFiles = []string{"cert.pem", "key.pem", "fullchain.pem"}

// ApplyCert 应用一次证书更新（CLI 与 Daemon 共用），顺序固定：
//  1. cert/key/fullchain 保存到 <workDir>/<domain>/（空内容跳过，其余文件名忽略；
//     工作目录写入不跟随软链接，预置的软链接会被替换为普通文件）
//  2. 部署到站点路径（site 为 nil 时跳过）
//  3. 最后写 time.log：部署失败时不写，避免下次同步被误判为已是最新
func ApplyCert(workDir, domain string, files map[string][]byte, site *config.SiteDeployConfig) error {
	domainDir, err := cert.SafeDomainDir(workDir, domain)
	if err != nil {
		return err
	}

	for _, name := range certFiles {
		if len(files[name]) == 0 {
			continue
		}
		if err := cert.WriteFileAtomic(filepath.Join(domainDir, name), files[name], cert.CertFilePerm(name), false); err != nil {
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
		if err := cert.WriteFileAtomic(filepath.Join(domainDir, "time.log"), timeLog, cert.PermCert, false); err != nil {
			return fmt.Errorf("保存 time.log 失败: %w", err)
		}
	}
	return nil
}

// DeploySite 将证书写入站点配置的目标路径（路径中的 {domain} 替换为实际域名，未配置的路径跳过）。
// 先整体校验：任一配置目标的源内容缺失或为空即拒绝，不做部分写入；再逐个原子写入。
// 目标为软链接时写入其真实文件（悬空软链接报错）；目标已存在时保留原权限与属主（见 cert.WriteFileAtomic）。
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
		if err := cert.WriteFileAtomic(dst, files[name], cert.CertFilePerm(name), true); err != nil {
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

// IsCertUpToDate 判断本地证书是否无需更新：服务端时间戳有效且本地时间戳不旧于服务端
func IsCertUpToDate(localTS, serverTS int64) bool {
	return serverTS > 0 && localTS >= serverTS
}
