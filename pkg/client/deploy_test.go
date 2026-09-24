package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Catker/acmeDeliver/pkg/config"
)

// testCertFiles 构造内存中的三份证书源文件
func testCertFiles(cert, key, fullchain string) map[string][]byte {
	return map[string][]byte{
		"cert.pem":      []byte(cert),
		"key.pem":       []byte(key),
		"fullchain.pem": []byte(fullchain),
	}
}

func TestDeploySite_WritesContentAndPerms(t *testing.T) {
	dstDir := t.TempDir()
	site := &config.SiteDeployConfig{
		CertPath:      filepath.Join(dstDir, "{domain}", "cert.pem"),
		KeyPath:       filepath.Join(dstDir, "{domain}", "key.pem"),
		FullchainPath: filepath.Join(dstDir, "{domain}", "fullchain.pem"),
	}

	if err := DeploySite(site, "example.com", testCertFiles("cert-data", "key-data", "chain-data")); err != nil {
		t.Fatalf("DeploySite() error = %v", err)
	}

	// {domain} 占位符替换 + 内容 + 权限（key.pem 0600，其余 0644）
	assertFileContentPerm(t, filepath.Join(dstDir, "example.com", "cert.pem"), "cert-data", 0644)
	assertFileContentPerm(t, filepath.Join(dstDir, "example.com", "key.pem"), "key-data", 0600)
	assertFileContentPerm(t, filepath.Join(dstDir, "example.com", "fullchain.pem"), "chain-data", 0644)
}

// 工作目录中预置的软链接不得被跟随：被链接文件不变，key.pem 变为普通文件（0600）
func TestApplyCert_WorkDirDoesNotFollowSymlink(t *testing.T) {
	workDir := t.TempDir()
	domainDir := filepath.Join(workDir, "example.com")
	if err := os.MkdirAll(domainDir, 0755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "victim.txt")
	if err := os.WriteFile(victim, []byte("victim-data"), 0644); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(domainDir, "key.pem")
	if err := os.Symlink(victim, keyPath); err != nil {
		t.Fatal(err)
	}

	if err := ApplyCert(workDir, "example.com", testCertFiles("cert-data", "new-key", "chain-data"), nil); err != nil {
		t.Fatalf("ApplyCert() error = %v", err)
	}

	assertFileContentPerm(t, victim, "victim-data", 0644)
	info, err := os.Lstat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatal("工作目录 key.pem 应被替换为普通文件")
	}
	assertFileContentPerm(t, keyPath, "new-key", 0600)
}

func TestDeploySite_PartialConfig(t *testing.T) {
	dstDir := t.TempDir()
	site := &config.SiteDeployConfig{CertPath: filepath.Join(dstDir, "cert.pem")}

	// 未配置的 key/fullchain 即使源内容为空也不影响部署
	if err := DeploySite(site, "example.com", map[string][]byte{"cert.pem": []byte("cert only")}); err != nil {
		t.Fatalf("DeploySite() error = %v", err)
	}
	assertFileContentPerm(t, filepath.Join(dstDir, "cert.pem"), "cert only", 0644)
	if _, err := os.Stat(filepath.Join(dstDir, "key.pem")); !os.IsNotExist(err) {
		t.Error("未配置 key_path 时不应写入 key.pem")
	}
}

// 空源文件（缺失或空内容）必须整体拒绝部署，不得部分写入或清空已有目标
func TestDeploySite_EmptySourceRejectedWithoutPartialWrite(t *testing.T) {
	for _, emptyName := range []string{"cert.pem", "key.pem", "fullchain.pem"} {
		t.Run(emptyName, func(t *testing.T) {
			files := testCertFiles("cert-data", "key-data", "chain-data")
			files[emptyName] = []byte{}

			dstDir := t.TempDir()
			sentinel := map[string]string{
				"cert.pem":      "existing-cert",
				"key.pem":       "existing-key",
				"fullchain.pem": "existing-chain",
			}
			for name, content := range sentinel {
				if err := os.WriteFile(filepath.Join(dstDir, name), []byte(content), 0644); err != nil {
					t.Fatal(err)
				}
			}
			site := &config.SiteDeployConfig{
				CertPath:      filepath.Join(dstDir, "cert.pem"),
				KeyPath:       filepath.Join(dstDir, "key.pem"),
				FullchainPath: filepath.Join(dstDir, "fullchain.pem"),
			}

			err := DeploySite(site, "example.com", files)
			if err == nil || !strings.Contains(err.Error(), emptyName) {
				t.Fatalf("源文件 %s 为空时应返回指明该文件的错误，得到 %v", emptyName, err)
			}
			for name, want := range sentinel {
				assertFileContentPerm(t, filepath.Join(dstDir, name), want, 0644)
			}
		})
	}
}

func TestReadLocalTimestamp(t *testing.T) {
	workDir := t.TempDir()
	if got := ReadLocalTimestamp(workDir, "example.com"); got != 0 {
		t.Fatalf("time.log 不存在时应返回 0，得到 %d", got)
	}
	if err := os.MkdirAll(filepath.Join(workDir, "example.com"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "example.com", "time.log"), []byte("1757011200\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := ReadLocalTimestamp(workDir, "example.com"); got != 1757011200 {
		t.Fatalf("ReadLocalTimestamp() = %d, want 1757011200", got)
	}
}

func TestIsCertUpToDate(t *testing.T) {
	tests := []struct {
		name     string
		localTS  int64
		serverTS int64
		want     bool
	}{
		{"本地与服务端相同则跳过", 100, 100, true},
		{"本地较新则跳过", 200, 100, true},
		{"本地较旧需更新", 99, 100, false},
		{"本地无 time.log 需更新", 0, 100, false},
		{"服务端无时间戳需更新", 100, 0, false},
		{"双方都无时间戳需更新", 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsCertUpToDate(tt.localTS, tt.serverTS); got != tt.want {
				t.Errorf("IsCertUpToDate(%d, %d) = %v, want %v", tt.localTS, tt.serverTS, got, tt.want)
			}
		})
	}
}
