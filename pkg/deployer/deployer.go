package deployer

import (
	"fmt"
	"strings"

	"log/slog"

	"github.com/Catker/acmeDeliver/pkg/cert"
	"github.com/Catker/acmeDeliver/pkg/client"
)

// DeploymentConfig 部署配置
// reload 命令不在部署器内执行：CLI 批处理与 Daemon 防抖是仅有的 reload 入口，
// 以保留去重、超时与统一日志行为
type DeploymentConfig struct {
	Domain        string // 当前部署的域名（用于 {domain} 占位符替换）
	CertPath      string `yaml:"cert_path"`      // 证书路径（可选，支持 {domain} 占位符）
	KeyPath       string `yaml:"key_path"`       // 私钥路径（可选，支持 {domain} 占位符）
	FullchainPath string `yaml:"fullchain_path"` // 证书链路径（可选，支持 {domain} 占位符）
}

// Deployer 定义了部署证书的标准接口
type Deployer interface {
	Deploy(certs *client.CertificateFiles, dryRun bool) error
}

// NewDeployer 创建部署器
// 配置驱动：如果配置了任何路径就部署，否则跳过
func NewDeployer(cfg DeploymentConfig) Deployer {
	// 如果没有配置任何路径，返回 NoOpDeployer
	if cfg.CertPath == "" && cfg.KeyPath == "" && cfg.FullchainPath == "" {
		slog.Debug("未配置任何部署路径，跳过部署")
		return &NoOpDeployer{}
	}

	return &ConfigDrivenDeployer{cfg: cfg}
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
		return nil
	}

	slog.Info("开始部署证书", "domain", d.cfg.Domain)

	// 先整体校验：任一配置目标的源内容为空即拒绝，不做部分写入（与 Daemon 共用规则）
	if certPath != "" {
		if err := cert.CheckDeployContent("cert.pem", certs.Cert); err != nil {
			return err
		}
	}
	if keyPath != "" {
		if err := cert.CheckDeployContent("key.pem", certs.Key); err != nil {
			return err
		}
	}
	if fullchainPath != "" {
		if err := cert.CheckDeployContent("fullchain.pem", certs.Fullchain); err != nil {
			return err
		}
	}

	// 写入证书文件（如果配置了）
	if certPath != "" {
		if err := cert.WriteFileAtomic(certPath, certs.Cert, cert.PermCert); err != nil {
			return fmt.Errorf("写入证书文件失败: %w", err)
		}
		slog.Info("证书已写入", "path", certPath)
	}

	// 写入私钥文件（如果配置了）
	if keyPath != "" {
		if err := cert.WriteFileAtomic(keyPath, certs.Key, cert.PermKey); err != nil {
			return fmt.Errorf("写入私钥文件失败: %w", err)
		}
		slog.Info("私钥已写入", "path", keyPath)
	}

	// 写入证书链文件（如果配置了）
	if fullchainPath != "" {
		if err := cert.WriteFileAtomic(fullchainPath, certs.Fullchain, cert.PermCert); err != nil {
			return fmt.Errorf("写入证书链文件失败: %w", err)
		}
		slog.Info("证书链已写入", "path", fullchainPath)
	}

	slog.Info("证书部署完成", "domain", d.cfg.Domain)
	return nil
}
