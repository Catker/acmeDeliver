package client

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// safeDomainDir 校验域名并返回工作目录下的安全子目录
func safeDomainDir(baseDir, domain string) (string, error) {
	if domain == "" {
		return "", errors.New("empty domain")
	}
	if strings.Contains(domain, "/") || strings.Contains(domain, "\\") || strings.Contains(domain, "..") {
		return "", errors.New("invalid domain path")
	}

	domainDir := filepath.Join(baseDir, domain)
	if err := ensurePathWithinBase(baseDir, domainDir); err != nil {
		return "", err
	}

	return domainDir, nil
}

// safeDomainFilePath 校验文件名并返回域名目录下的安全文件路径
func safeDomainFilePath(baseDir, domain, filename string) (string, error) {
	domainDir, err := safeDomainDir(baseDir, domain)
	if err != nil {
		return "", err
	}
	if err := validateRelativeFileName(filename); err != nil {
		return "", err
	}

	filePath := filepath.Join(domainDir, filename)
	if err := ensurePathWithinBase(domainDir, filePath); err != nil {
		return "", err
	}

	return filePath, nil
}

func validateRelativeFileName(filename string) error {
	if filename == "" {
		return errors.New("empty filename")
	}
	if strings.Contains(filename, "/") || strings.Contains(filename, "\\") || strings.Contains(filename, "..") {
		return fmt.Errorf("invalid filename: %s", filename)
	}
	return nil
}

func ensurePathWithinBase(baseDir, targetPath string) error {
	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return err
	}
	absTarget, err := filepath.Abs(targetPath)
	if err != nil {
		return err
	}

	baseWithSep := absBase + string(filepath.Separator)
	if absTarget != absBase && !strings.HasPrefix(absTarget, baseWithSep) {
		return errors.New("path escapes base dir")
	}

	return nil
}
