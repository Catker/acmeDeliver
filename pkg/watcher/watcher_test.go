package watcher

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/Catker/acmeDeliver/pkg/cert"
)

func TestCertWatcher_ReadCertFiles(t *testing.T) {
	tmpDir := t.TempDir()
	domain := "example.com"
	domainPath := filepath.Join(tmpDir, domain)

	// 创建域名目录
	if err := os.MkdirAll(domainPath, 0755); err != nil {
		t.Fatalf("创建域名目录失败: %v", err)
	}

	// 创建测试证书文件
	testFiles := map[string]string{
		"cert.pem":      "-----BEGIN CERTIFICATE-----\ntest cert\n-----END CERTIFICATE-----",
		"key.pem":       "-----BEGIN PRIVATE KEY-----\ntest key\n-----END PRIVATE KEY-----",
		"fullchain.pem": "-----BEGIN CERTIFICATE-----\ntest fullchain\n-----END CERTIFICATE-----",
		"time.log":      "1234567890",        // 时间戳文件，需要同步
		"readme.txt":    "should be ignored", // 非证书文件，应被忽略
		// acme.sh 目录中的其他证书/私钥副本不下发
		"example.com.key": "dup key",
		"example.com.cer": "dup cert",
		"ca.cer":          "ca",
		"chain.pem":       "chain",
	}

	for name, content := range testFiles {
		path := filepath.Join(domainPath, name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("创建测试文件 %s 失败: %v", name, err)
		}
	}

	watcher, err := NewCertWatcher(tmpDir, time.Second)
	if err != nil {
		t.Fatalf("NewCertWatcher() error = %v", err)
	}
	defer watcher.Stop()

	files, err := watcher.certs.Load(domain)
	if err != nil {
		t.Fatalf("certs.Load() error = %v", err)
	}

	// 验证只读取了证书相关文件（包含 time.log）
	expectedFiles := []string{"cert.pem", "key.pem", "fullchain.pem", "time.log"}
	if len(files) != len(expectedFiles) {
		t.Errorf("certs.Load() 返回 %d 个文件，期望 %d 个", len(files), len(expectedFiles))
	}

	for _, name := range expectedFiles {
		if _, ok := files[name]; !ok {
			t.Errorf("certs.Load() 缺少文件: %s", name)
		}
	}

	// 非下发文件不应被包含
	for _, name := range []string{"readme.txt", "example.com.key", "example.com.cer", "ca.cer", "chain.pem"} {
		if _, ok := files[name]; ok {
			t.Errorf("certs.Load() 不应包含 %s", name)
		}
	}

	// 验证文件内容
	if string(files["cert.pem"]) != testFiles["cert.pem"] {
		t.Error("cert.pem 内容不匹配")
	}
}

func TestCertWatcher_ReadCertFiles_EmptyDir(t *testing.T) {
	tmpDir := t.TempDir()
	domain := "empty.com"
	domainPath := filepath.Join(tmpDir, domain)

	if err := os.MkdirAll(domainPath, 0755); err != nil {
		t.Fatalf("创建域名目录失败: %v", err)
	}

	watcher, err := NewCertWatcher(tmpDir, time.Second)
	if err != nil {
		t.Fatalf("NewCertWatcher() error = %v", err)
	}
	defer watcher.Stop()

	files, err := watcher.certs.Load(domain)
	if !errors.Is(err, cert.ErrNoFiles) {
		t.Fatalf("certs.Load() error = %v, want %v", err, cert.ErrNoFiles)
	}

	if len(files) != 0 {
		t.Errorf("certs.Load() 应返回空结果，实际返回 %d 个文件", len(files))
	}
}

func TestCertWatcher_ReadCertFiles_NonExistDir(t *testing.T) {
	tmpDir := t.TempDir()

	watcher, err := NewCertWatcher(tmpDir, time.Second)
	if err != nil {
		t.Fatalf("NewCertWatcher() error = %v", err)
	}
	defer watcher.Stop()

	_, err = watcher.certs.Load("nonexistent.com")
	if !errors.Is(err, cert.ErrDomainNotFound) {
		t.Errorf("certs.Load() 目录不存在时 error = %v, want %v", err, cert.ErrDomainNotFound)
	}
}

func TestCertWatcher_HandleEvent_NewDomainDirAddsWatch(t *testing.T) {
	tmpDir := t.TempDir()

	watcher, err := NewCertWatcher(tmpDir, time.Second)
	if err != nil {
		t.Fatalf("NewCertWatcher() error = %v", err)
	}
	defer watcher.Stop()

	if err := watcher.watcher.Add(tmpDir); err != nil {
		t.Fatalf("watcher.Add(%q) error = %v", tmpDir, err)
	}

	domain := "example.com"
	domainPath := filepath.Join(tmpDir, domain)
	if err := os.Mkdir(domainPath, 0755); err != nil {
		t.Fatalf("创建新域名目录失败: %v", err)
	}

	pending := make(map[string]time.Time)
	watcher.handleEvent(fsnotify.Event{
		Name: domainPath,
		Op:   fsnotify.Create,
	}, pending)

	if _, ok := pending[domain]; !ok {
		t.Fatalf("pending 中缺少新域名 %q", domain)
	}

	if !containsWatch(watcher.watcher.WatchList(), domainPath) {
		t.Fatalf("新域名目录 %q 未加入 watcher", domainPath)
	}
}

func TestCertWatcher_HandleEvent_IgnoresBaseDirFile(t *testing.T) {
	tmpDir := t.TempDir()

	watcher, err := NewCertWatcher(tmpDir, time.Second)
	if err != nil {
		t.Fatalf("NewCertWatcher() error = %v", err)
	}
	defer watcher.Stop()

	filePath := filepath.Join(tmpDir, "readme.txt")
	if err := os.WriteFile(filePath, []byte("test"), 0644); err != nil {
		t.Fatalf("创建测试文件失败: %v", err)
	}

	pending := make(map[string]time.Time)
	watcher.handleEvent(fsnotify.Event{
		Name: filePath,
		Op:   fsnotify.Create,
	}, pending)

	if len(pending) != 0 {
		t.Fatalf("baseDir 下普通文件事件不应生成 pending，实际为 %v", pending)
	}
}

func TestCertWatcher_HandleEvent_DomainDirFileFilter(t *testing.T) {
	tmpDir := t.TempDir()

	watcher, err := NewCertWatcher(tmpDir, time.Second)
	if err != nil {
		t.Fatalf("NewCertWatcher() error = %v", err)
	}
	defer watcher.Stop()

	domain := "example.com"
	tests := []struct {
		name string
		file string
		op   fsnotify.Op
		want bool
	}{
		{"cert.pem 写入触发", "cert.pem", fsnotify.Write, true},
		{"key.pem 原子替换(Create)触发", "key.pem", fsnotify.Create, true},
		{"fullchain.pem 写入触发", "fullchain.pem", fsnotify.Write, true},
		{"time.log 写入触发", "time.log", fsnotify.Write, true},
		{"原子写入临时文件忽略", "cert.pem.tmp-123456", fsnotify.Create, false},
		{".conf 忽略", "example.com.conf", fsnotify.Write, false},
		{".csr 忽略", "example.com.csr", fsnotify.Create, false},
		{"其他证书副本忽略", "ca.cer", fsnotify.Write, false},
		{"下发文件删除忽略", "cert.pem", fsnotify.Remove, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pending := make(map[string]time.Time)
			watcher.handleEvent(fsnotify.Event{
				Name: filepath.Join(tmpDir, domain, tt.file),
				Op:   tt.op,
			}, pending)
			if _, got := pending[domain]; got != tt.want {
				t.Fatalf("pending[%q] 存在 = %v, want %v", domain, got, tt.want)
			}
		})
	}
}

func containsWatch(watches []string, target string) bool {
	for _, watch := range watches {
		if watch == target {
			return true
		}
	}
	return false
}
