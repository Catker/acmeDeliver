package cert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// ============================================
// 辅助函数 - 生成测试证书
// ============================================

// generateTestCert 生成测试用的自签名证书
func generateTestCert(notBefore, notAfter time.Time, cn string, issuerOrg string) ([]byte, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: cn,
		},
		Issuer: pkix.Name{
			CommonName:   issuerOrg,
			Organization: []string{issuerOrg},
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		return nil, err
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}), nil
}

// ============================================
// ParseCertificate 测试
// ============================================

func TestParseCertificate_Valid(t *testing.T) {
	notBefore := time.Now()
	notAfter := notBefore.Add(365 * 24 * time.Hour)

	certPEM, err := generateTestCert(notBefore, notAfter, "example.com", "Test CA")
	if err != nil {
		t.Fatalf("生成测试证书失败: %v", err)
	}

	cert, err := ParseCertificate(certPEM)
	if err != nil {
		t.Fatalf("ParseCertificate 失败: %v", err)
	}

	if cert.Subject.CommonName != "example.com" {
		t.Errorf("Subject.CommonName = %q, want %q", cert.Subject.CommonName, "example.com")
	}
}

func TestParseCertificate_InvalidPEM(t *testing.T) {
	invalidPEM := []byte("not a valid PEM data")

	_, err := ParseCertificate(invalidPEM)
	if err == nil {
		t.Error("期望返回错误，但返回了 nil")
	}
}

func TestParseCertificate_WrongType(t *testing.T) {
	// 创建一个非 CERTIFICATE 类型的 PEM 块
	wrongTypePEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: []byte("fake key data"),
	})

	_, err := ParseCertificate(wrongTypePEM)
	if err == nil {
		t.Error("期望返回错误，但返回了 nil")
	}
}

// ============================================
// CollectDomainStatus 测试
// ============================================

func TestCollectDomainStatus_Complete(t *testing.T) {
	// 创建临时目录结构
	tmpDir := t.TempDir()
	domain := "example.com"
	domainDir := filepath.Join(tmpDir, domain)
	if err := os.MkdirAll(domainDir, 0755); err != nil {
		t.Fatal(err)
	}

	// 生成测试证书
	notBefore := time.Now()
	notAfter := notBefore.Add(90 * 24 * time.Hour)
	certPEM, err := generateTestCert(notBefore, notAfter, domain, "Let's Encrypt")
	if err != nil {
		t.Fatal(err)
	}

	// 写入文件
	if err := os.WriteFile(filepath.Join(domainDir, "cert.pem"), certPEM, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(domainDir, "key.pem"), []byte("fake key"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(domainDir, "fullchain.pem"), certPEM, 0644); err != nil {
		t.Fatal(err)
	}

	// 写入 time.log
	timestamp := time.Now().Unix()
	if err := os.WriteFile(filepath.Join(domainDir, "time.log"), []byte(strconv.FormatInt(timestamp, 10)), 0644); err != nil {
		t.Fatal(err)
	}

	// 测试
	status := CollectDomainStatus(tmpDir, domain)

	if status.Domain != domain {
		t.Errorf("Domain = %q, want %q", status.Domain, domain)
	}
	if !status.HasCert {
		t.Error("HasCert = false, want true")
	}
	if !status.HasKey {
		t.Error("HasKey = false, want true")
	}
	if !status.HasFullchain {
		t.Error("HasFullchain = false, want true")
	}
	if !status.Valid {
		t.Error("Valid = false, want true")
	}
	if status.LastUpdate != timestamp {
		t.Errorf("LastUpdate = %d, want %d", status.LastUpdate, timestamp)
	}
	if status.DaysRemaining < 89 || status.DaysRemaining > 91 {
		t.Errorf("DaysRemaining = %d, 期望约 90 天", status.DaysRemaining)
	}
	if status.Subject != domain {
		t.Errorf("Subject = %q, want %q", status.Subject, domain)
	}
}

func TestCollectDomainStatus_MissingFiles(t *testing.T) {
	tmpDir := t.TempDir()
	domain := "missing.com"
	domainDir := filepath.Join(tmpDir, domain)
	if err := os.MkdirAll(domainDir, 0755); err != nil {
		t.Fatal(err)
	}

	// 只写入 cert.pem
	if err := os.WriteFile(filepath.Join(domainDir, "cert.pem"), []byte("cert"), 0644); err != nil {
		t.Fatal(err)
	}

	status := CollectDomainStatus(tmpDir, domain)

	if status.Valid {
		t.Error("Valid = true, want false (缺少 key 和 fullchain)")
	}
	if status.Error != "缺少必需文件" {
		t.Errorf("Error = %q, want %q", status.Error, "缺少必需文件")
	}
}

func TestCollectDomainStatus_EmptyFiles(t *testing.T) {
	tmpDir := t.TempDir()
	domain := "empty.com"
	domainDir := filepath.Join(tmpDir, domain)
	if err := os.MkdirAll(domainDir, 0755); err != nil {
		t.Fatal(err)
	}

	// 创建空文件
	for _, name := range []string{"cert.pem", "key.pem", "fullchain.pem"} {
		if err := os.WriteFile(filepath.Join(domainDir, name), []byte{}, 0644); err != nil {
			t.Fatal(err)
		}
	}

	status := CollectDomainStatus(tmpDir, domain)

	if status.Valid {
		t.Error("Valid = true, want false (文件为空)")
	}
	if status.Error != "文件为空" {
		t.Errorf("Error = %q, want %q", status.Error, "文件为空")
	}
}

func TestCollectDomainStatus_NonExistentDomain(t *testing.T) {
	tmpDir := t.TempDir()
	status := CollectDomainStatus(tmpDir, "nonexistent.com")

	if status.Valid {
		t.Error("Valid = true, want false")
	}
	if status.HasCert || status.HasKey || status.HasFullchain {
		t.Error("期望所有 Has* 字段都为 false")
	}
}

// ============================================
// CollectAllDomainStatus 测试
// ============================================

func TestCollectAllDomainStatus_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	statuses := CollectAllDomainStatus(tmpDir)

	if len(statuses) != 0 {
		t.Errorf("期望空切片，得到 %d 个元素", len(statuses))
	}
}

func TestCollectAllDomainStatus_MultipleDomains(t *testing.T) {
	tmpDir := t.TempDir()
	domains := []string{"a.com", "b.com", "c.com"}

	for _, domain := range domains {
		domainDir := filepath.Join(tmpDir, domain)
		if err := os.MkdirAll(domainDir, 0755); err != nil {
			t.Fatal(err)
		}
		// 创建占位文件
		if err := os.WriteFile(filepath.Join(domainDir, "cert.pem"), []byte("cert"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// 创建一个普通文件（非目录），应该被忽略
	if err := os.WriteFile(filepath.Join(tmpDir, "ignored.txt"), []byte("ignored"), 0644); err != nil {
		t.Fatal(err)
	}

	statuses := CollectAllDomainStatus(tmpDir)

	if len(statuses) != len(domains) {
		t.Errorf("期望 %d 个域名，得到 %d 个", len(domains), len(statuses))
	}
}

func TestCollectAllDomainStatus_InvalidDir(t *testing.T) {
	statuses := CollectAllDomainStatus("/nonexistent/path/12345")

	if statuses != nil {
		t.Error("期望返回 nil，实际返回非空切片")
	}
}
