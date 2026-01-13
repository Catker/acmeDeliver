package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"log/slog"

	"github.com/Catker/acmeDeliver/pkg/client"
	"github.com/nightlyone/lockfile"
)

// Workspace 管理客户端的工作目录
type Workspace struct {
	workDir   string
	domain    string
	domainDir string
}

// NewWorkspace 创建新的工作空间管理器
func NewWorkspace(workDir, domain string) *Workspace {
	domainDir := filepath.Join(workDir, domain)
	return &Workspace{
		workDir:   workDir,
		domain:    domain,
		domainDir: domainDir,
	}
}

// Ensure 确保工作目录存在
func (ws *Workspace) Ensure() error {
	// 创建主工作目录
	if err := os.MkdirAll(ws.workDir, 0755); err != nil {
		return fmt.Errorf("创建工作目录失败: %w", err)
	}

	// 创建域名目录
	if err := os.MkdirAll(ws.domainDir, 0755); err != nil {
		return fmt.Errorf("创建域名目录失败: %w", err)
	}

	slog.Info("工作目录已创建", "work_dir", ws.workDir, "domain", ws.domain)
	return nil
}

// GetDomainDir 获取域名工作目录
func (ws *Workspace) GetDomainDir() string {
	return ws.domainDir
}

// GetWorkDir 获取主工作目录
func (ws *Workspace) GetWorkDir() string {
	return ws.workDir
}

// validateFilename 验证文件名是否安全
func (ws *Workspace) validateFilename(filename string) error {
	// 检查路径遍历攻击
	if strings.Contains(filename, "..") || strings.Contains(filename, "\\") {
		return fmt.Errorf("不安全的文件名: %s", filename)
	}

	// 构建完整路径
	fullPath := filepath.Join(ws.domainDir, filename)

	// 获取绝对路径
	absFullPath, err := filepath.Abs(fullPath)
	if err != nil {
		return fmt.Errorf("无法解析文件路径: %w", err)
	}

	// 获取工作目录的绝对路径
	absDomainDir, err := filepath.Abs(ws.domainDir)
	if err != nil {
		return fmt.Errorf("无法解析工作目录路径: %w", err)
	}

	// 确保文件路径在工作目录内
	if !strings.HasPrefix(absFullPath, absDomainDir) {
		return fmt.Errorf("文件路径超出工作目录范围: %s", filename)
	}

	return nil
}

// SaveFile 保存文件到工作目录
func (ws *Workspace) SaveFile(filename string, content []byte) error {
	// 验证文件名安全性
	if err := ws.validateFilename(filename); err != nil {
		return err
	}

	filePath := filepath.Join(ws.domainDir, filename)

	// 先写入临时文件，然后原子性重命名
	tempPath := filePath + ".tmp"
	if err := os.WriteFile(tempPath, content, 0644); err != nil {
		return fmt.Errorf("写入临时文件失败: %w", err)
	}

	if err := os.Rename(tempPath, filePath); err != nil {
		os.Remove(tempPath) // 清理临时文件
		return fmt.Errorf("保存文件失败: %w", err)
	}

	slog.Info("文件已保存", "file", filename, "size", len(content))
	return nil
}

// SaveFileWithPerm 保存文件到工作目录（指定权限）
func (ws *Workspace) SaveFileWithPerm(filename string, content []byte, perm os.FileMode) error {
	// 验证文件名安全性
	if err := ws.validateFilename(filename); err != nil {
		return err
	}

	filePath := filepath.Join(ws.domainDir, filename)

	// 先写入临时文件，然后原子性重命名
	tempPath := filePath + ".tmp"
	if err := os.WriteFile(tempPath, content, perm); err != nil {
		return fmt.Errorf("写入临时文件失败: %w", err)
	}

	if err := os.Rename(tempPath, filePath); err != nil {
		os.Remove(tempPath) // 清理临时文件
		return fmt.Errorf("保存文件失败: %w", err)
	}

	slog.Info("文件已保存", "file", filename, "size", len(content), "perm", perm)
	return nil
}

// Lock 获取文件锁，防止并发操作
func (ws *Workspace) Lock() (*lockfile.Lockfile, error) {
	lockFilePath := filepath.Join(ws.domainDir, ".lock")
	fileLock, err := lockfile.New(lockFilePath)
	if err != nil {
		return nil, fmt.Errorf("创建文件锁失败: %w", err)
	}

	// 尝试获取锁
	if err := fileLock.TryLock(); err != nil {
		slog.Info("另一个实例正在运行", "domain", ws.domain, "error", err)
		return nil, fmt.Errorf("另一个实例正在运行: %w", err)
	}

	slog.Debug("获取文件锁成功", "domain", ws.domain)
	return &fileLock, nil
}

// SaveCertificateFiles 保存所有证书文件
func (ws *Workspace) SaveCertificateFiles(certs *client.CertificateFiles) error {
	files := map[string][]byte{
		"cert.pem":      certs.Cert,
		"key.pem":       certs.Key,
		"fullchain.pem": certs.Fullchain,
	}

	for filename, content := range files {
		if len(content) == 0 {
			slog.Warn("证书文件内容为空，跳过", "file", filename)
			continue
		}

		// 确定文件权限：私钥文件使用更严格的权限
		var perm os.FileMode = 0644
		if filename == "key.pem" {
			perm = 0600 // 私钥只有所有者可读写
		}

		if err := ws.SaveFileWithPerm(filename, content, perm); err != nil {
			return fmt.Errorf("保存文件 %s 失败: %w", filename, err)
		}
	}

	slog.Info("所有证书文件已保存", "domain", ws.domain)
	return nil
}
