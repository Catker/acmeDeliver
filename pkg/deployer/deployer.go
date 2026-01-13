package deployer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"log/slog"

	"github.com/Catker/acmeDeliver/pkg/client"
	"github.com/Catker/acmeDeliver/pkg/command"
)

// DeploymentConfig 部署配置
type DeploymentConfig struct {
	Domain        string // 当前部署的域名（用于 {domain} 占位符替换）
	CertPath      string `yaml:"cert_path"`      // 证书路径（可选，支持 {domain} 占位符）
	KeyPath       string `yaml:"key_path"`       // 私钥路径（可选，支持 {domain} 占位符）
	FullchainPath string `yaml:"fullchain_path"` // 证书链路径（可选，支持 {domain} 占位符）
	ReloadCmd     string `yaml:"reloadcmd"`      // 重载命令（可选）
	SkipReload    bool   // 跳过 reload（批量部署时使用，最后统一执行）
}

// Deployer 定义了部署证书的标准接口
type Deployer interface {
	Deploy(certs *client.CertificateFiles, dryRun bool) error
}

// NewDeployer 创建部署器
// 配置驱动：如果配置了任何路径就部署，否则跳过
func NewDeployer(cfg DeploymentConfig) (Deployer, error) {
	// 如果没有配置任何路径，返回 NoOpDeployer
	if cfg.CertPath == "" && cfg.KeyPath == "" && cfg.FullchainPath == "" {
		slog.Debug("未配置任何部署路径，跳过部署")
		return &NoOpDeployer{}, nil
	}

	return &ConfigDrivenDeployer{cfg: cfg}, nil
}

// NoOpDeployer 空操作部署器（仅更新，不部署）
type NoOpDeployer struct{}

func (d *NoOpDeployer) Deploy(certs *client.CertificateFiles, dryRun bool) error {
	return nil
}

// ConfigDrivenDeployer 配置驱动的部署器
// 根据配置的路径决定写入哪些文件
type ConfigDrivenDeployer struct {
	cfg DeploymentConfig
}

// replacePath 替换路径中的 {domain} 占位符
func (d *ConfigDrivenDeployer) replacePath(path string) string {
	if d.cfg.Domain == "" {
		return path
	}
	return strings.ReplaceAll(path, "{domain}", d.cfg.Domain)
}

func (d *ConfigDrivenDeployer) Deploy(certs *client.CertificateFiles, dryRun bool) error {
	// 预处理路径，替换占位符
	certPath := d.replacePath(d.cfg.CertPath)
	keyPath := d.replacePath(d.cfg.KeyPath)
	fullchainPath := d.replacePath(d.cfg.FullchainPath)

	if dryRun {
		slog.Info("[DryRun] 配置驱动部署模式 - 将要执行以下操作:", "domain", d.cfg.Domain)
		if certPath != "" {
			slog.Info("[DryRun] 写入证书文件", "path", certPath, "size", len(certs.Cert))
		}
		if keyPath != "" {
			slog.Info("[DryRun] 写入私钥文件", "path", keyPath, "size", len(certs.Key))
		}
		if fullchainPath != "" {
			slog.Info("[DryRun] 写入证书链文件", "path", fullchainPath, "size", len(certs.Fullchain))
		}
		if d.cfg.ReloadCmd != "" {
			slog.Info("[DryRun] 执行重载命令", "command", d.cfg.ReloadCmd)
		}
		return nil
	}

	slog.Info("开始部署证书", "domain", d.cfg.Domain)

	// 写入证书文件（如果配置了）
	if certPath != "" {
		if len(certs.Cert) == 0 {
			return fmt.Errorf("证书内容为空，无法写入 cert_path")
		}
		if err := d.writeFile(certPath, certs.Cert); err != nil {
			return fmt.Errorf("写入证书文件失败: %w", err)
		}
		slog.Info("证书已写入", "path", certPath)
	}

	// 写入私钥文件（如果配置了）
	if keyPath != "" {
		if len(certs.Key) == 0 {
			return fmt.Errorf("私钥内容为空，无法写入 key_path")
		}
		if err := d.writeFile(keyPath, certs.Key); err != nil {
			return fmt.Errorf("写入私钥文件失败: %w", err)
		}
		slog.Info("私钥已写入", "path", keyPath)
	}

	// 写入证书链文件（如果配置了）
	if fullchainPath != "" {
		if len(certs.Fullchain) == 0 {
			return fmt.Errorf("证书链内容为空，无法写入 fullchain_path")
		}
		if err := d.writeFile(fullchainPath, certs.Fullchain); err != nil {
			return fmt.Errorf("写入证书链文件失败: %w", err)
		}
		slog.Info("证书链已写入", "path", fullchainPath)
	}

	// 执行重载命令（如果配置了且不跳过）
	if d.cfg.ReloadCmd != "" && !d.cfg.SkipReload {
		if err := d.runReloadCmd(); err != nil {
			return fmt.Errorf("执行重载命令失败: %w", err)
		}
	}

	slog.Info("证书部署完成", "domain", d.cfg.Domain)
	return nil
}

// writeFile 安全地写入文件，设置正确的权限
func (d *ConfigDrivenDeployer) writeFile(path string, content []byte) error {
	if path == "" {
		return fmt.Errorf("文件路径不能为空")
	}
	if len(content) == 0 {
		return fmt.Errorf("文件内容为空")
	}

	// 确保目录存在
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}

	// 写入临时文件然后重命名，确保原子性
	tempPath := path + ".tmp"
	if err := os.WriteFile(tempPath, content, 0644); err != nil {
		return fmt.Errorf("写入临时文件失败: %w", err)
	}

	if err := os.Rename(tempPath, path); err != nil {
		os.Remove(tempPath) // 清理临时文件
		return fmt.Errorf("重命名文件失败: %w", err)
	}

	return nil
}

// runReloadCmd 执行重载命令（15秒超时）
// 委托给 command.Execute 实现，避免代码重复
func (d *ConfigDrivenDeployer) runReloadCmd() error {
	if d.cfg.ReloadCmd == "" {
		return nil
	}

	slog.Info("执行重载命令", "cmd", d.cfg.ReloadCmd)

	output, err := command.Execute(context.Background(), d.cfg.ReloadCmd, 15*time.Second)
	if err != nil {
		slog.Error("重载命令执行失败", "error", err, "output", output)
		return fmt.Errorf("重载命令失败: %w", err)
	}

	slog.Info("重载命令执行成功", "output", output)
	return nil
}
