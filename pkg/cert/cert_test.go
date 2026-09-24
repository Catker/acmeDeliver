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

// ============================================
// WriteFileAtomic / CertFilePerm / CheckDeployContent 测试
// ============================================

func TestCertFilePerm(t *testing.T) {
	tests := []struct {
		filename string
		want     os.FileMode
	}{
		{"key.pem", 0600},
		{"server.key", 0600},
		{"cert.pem", 0644},
		{"fullchain.pem", 0644},
		{"time.log", 0644},
	}
	for _, tt := range tests {
		if got := CertFilePerm(tt.filename); got != tt.want {
			t.Errorf("CertFilePerm(%q) = %o, want %o", tt.filename, got, tt.want)
		}
	}
}

func TestSafeDomainDir(t *testing.T) {
	baseDir := t.TempDir()
	for _, bad := range []string{"", "../etc", "a/b", `a\b`, ".."} {
		if got, err := SafeDomainDir(baseDir, bad); err == nil {
			t.Errorf("SafeDomainDir(%q) 应返回错误，得到 %q", bad, got)
		}
	}
	got, err := SafeDomainDir(baseDir, "example.com")
	if err != nil || got != filepath.Join(baseDir, "example.com") {
		t.Errorf("SafeDomainDir(example.com) = %q, %v", got, err)
	}
}

// leftoverTemps 返回目录中残留的原子写入临时文件（唯一名 base.tmp-*）
func leftoverTemps(t *testing.T, dir, base string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, base+".tmp-*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	return matches
}

func TestWriteFileAtomic_CreatesAndReplaces(t *testing.T) {
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "sub")
	path := filepath.Join(subDir, "key.pem")

	if err := WriteFileAtomic(path, []byte("v1"), PermKey); err != nil {
		t.Fatalf("WriteFileAtomic() error = %v", err)
	}
	assertContentPerm(t, path, "v1", 0600)

	// 原子替换已有文件：内容与权限都应更新，且不残留临时文件
	if err := os.WriteFile(path, []byte("stale"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, []byte("v2"), PermKey); err != nil {
		t.Fatalf("WriteFileAtomic() overwrite error = %v", err)
	}
	assertContentPerm(t, path, "v2", 0600)
	if got := leftoverTemps(t, subDir, "key.pem"); len(got) != 0 {
		t.Errorf("原子替换后不应残留临时文件，发现: %v", got)
	}
}

func TestWriteFileAtomic_EmptyPath(t *testing.T) {
	if err := WriteFileAtomic("", []byte("x"), PermCert); err == nil {
		t.Error("空路径应返回错误")
	}
}

func TestWriteFileAtomic_TargetIsDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	blocker := filepath.Join(tmpDir, "blocker")
	if err := os.MkdirAll(blocker, 0755); err != nil {
		t.Fatal(err)
	}

	if err := WriteFileAtomic(blocker, []byte("x"), PermCert); err == nil {
		t.Fatal("目标是目录时应返回错误")
	}
	if got := leftoverTemps(t, tmpDir, "blocker"); len(got) != 0 {
		t.Errorf("失败后应清理自己创建的临时文件，残留: %v", got)
	}
}

// 回归：不得误删他人文件。固定名 path+".tmp" 属于其他写入者/用户，
// 原子写入使用唯一临时文件，该文件必须原样保留
func TestWriteFileAtomic_DoesNotTouchForeignTempFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "key.pem")
	foreign := path + ".tmp"

	if err := os.WriteFile(foreign, []byte("foreign-content"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := WriteFileAtomic(path, []byte("key"), PermKey); err != nil {
		t.Fatalf("WriteFileAtomic() error = %v", err)
	}
	assertContentPerm(t, path, "key", 0600)

	// 他人文件原样保留：内容与权限都不受影响
	assertContentPerm(t, foreign, "foreign-content", 0644)
}

// 回归：并发写入同一目标互不干扰，完成后无临时文件残留
func TestWriteFileAtomic_ConcurrentWritersDoNotCollide(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "cert.pem")

	const writers = 8
	done := make(chan error, writers)
	for i := 0; i < writers; i++ {
		go func() {
			done <- WriteFileAtomic(path, []byte("writer"), PermCert)
		}()
	}
	for i := 0; i < writers; i++ {
		if err := <-done; err != nil {
			t.Fatalf("并发写入失败: %v", err)
		}
	}

	assertContentPerm(t, path, "writer", 0644)
	if got := leftoverTemps(t, tmpDir, "cert.pem"); len(got) != 0 {
		t.Errorf("并发写入完成后不应残留临时文件，发现: %v", got)
	}
}

func assertContentPerm(t *testing.T, path, wantContent string, wantPerm os.FileMode) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != wantContent {
		t.Errorf("文件 %s 内容 = %q, want %q", path, string(data), wantContent)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if info.Mode().Perm() != wantPerm {
		t.Errorf("文件 %s 权限 = %o, want %o", path, info.Mode().Perm(), wantPerm)
	}
}

// ============================================
// ParseTimeLog 测试
// ============================================

func TestParseTimeLog(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    int64
	}{
		{name: "10 位秒级时间戳", content: "1757011200", want: 1757011200},
		{name: "尾随换行", content: "1757011200\n", want: 1757011200},
		{name: "毫秒级时间戳截取前 10 位", content: "1757011200123", want: 1757011200},
		{name: "前后空白", content: "  1757011200  \n", want: 1757011200},
		// 先 TrimSpace 再截取：带前导空白的 11 位输入不会被空格挤掉末位
		{name: "前导空白的毫秒时间戳", content: "  1757011200123", want: 1757011200},
		{name: "非数字", content: "not-a-timestamp", want: 0},
		{name: "空内容", content: "", want: 0},
		{name: "过短数字", content: "123", want: 123},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseTimeLog([]byte(tt.content)); got != tt.want {
				t.Errorf("ParseTimeLog(%q) = %d, want %d", tt.content, got, tt.want)
			}
		})
	}
}
