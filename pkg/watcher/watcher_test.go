package watcher

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

func TestNewCertWatcher(t *testing.T) {
	tmpDir := t.TempDir()

	watcher, err := NewCertWatcher(tmpDir, 2*time.Second)
	if err != nil {
		t.Fatalf("NewCertWatcher() error = %v", err)
	}
	defer watcher.Stop()

	if watcher.baseDir != tmpDir {
		t.Errorf("watcher.baseDir = %q, want %q", watcher.baseDir, tmpDir)
	}
	if watcher.debounce != 2*time.Second {
		t.Errorf("watcher.debounce = %v, want 2s", watcher.debounce)
	}
	if watcher.watcher == nil {
		t.Error("watcher.watcher 不应为 nil")
	}
	if watcher.lastUpdate == nil {
		t.Error("watcher.lastUpdate 不应为 nil")
	}
	if watcher.stop == nil {
		t.Error("watcher.stop 不应为 nil")
	}
}

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

	files, err := watcher.readCertFiles(domain)
	if err != nil {
		t.Fatalf("readCertFiles() error = %v", err)
	}

	// 验证只读取了证书相关文件（包含 time.log）
	expectedFiles := []string{"cert.pem", "key.pem", "fullchain.pem", "time.log"}
	if len(files) != len(expectedFiles) {
		t.Errorf("readCertFiles() 返回 %d 个文件，期望 %d 个", len(files), len(expectedFiles))
	}

	for _, name := range expectedFiles {
		if _, ok := files[name]; !ok {
			t.Errorf("readCertFiles() 缺少文件: %s", name)
		}
	}

	// 非下发文件不应被包含
	for _, name := range []string{"readme.txt", "example.com.key", "example.com.cer", "ca.cer", "chain.pem"} {
		if _, ok := files[name]; ok {
			t.Errorf("readCertFiles() 不应包含 %s", name)
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

	files, err := watcher.readCertFiles(domain)
	if err != nil {
		t.Fatalf("readCertFiles() error = %v", err)
	}

	if len(files) != 0 {
		t.Errorf("readCertFiles() 应返回空 map，实际返回 %d 个文件", len(files))
	}
}

func TestCertWatcher_ReadCertFiles_NonExistDir(t *testing.T) {
	tmpDir := t.TempDir()

	watcher, err := NewCertWatcher(tmpDir, time.Second)
	if err != nil {
		t.Fatalf("NewCertWatcher() error = %v", err)
	}
	defer watcher.Stop()

	_, err = watcher.readCertFiles("nonexistent.com")
	if err == nil {
		t.Error("readCertFiles() 应在目录不存在时返回错误")
	}
}

func TestCertWatcher_OnChange(t *testing.T) {
	tmpDir := t.TempDir()

	watcher, err := NewCertWatcher(tmpDir, time.Second)
	if err != nil {
		t.Fatalf("NewCertWatcher() error = %v", err)
	}
	defer watcher.Stop()

	watcher.OnChange(func(domain string, files map[string][]byte) {
		// 回调函数
	})

	if watcher.onChange == nil {
		t.Error("OnChange() 应设置回调函数")
	}
}

func TestCertWatcher_HandleEvent_NewDomainDirAddsWatch(t *testing.T) {
	tmpDir := t.TempDir()

	watcher, err := NewCertWatcher(tmpDir, time.Second)
	if err != nil {
		t.Fatalf("NewCertWatcher() error = %v", err)
	}
	defer watcher.Stop()

	if err := watcher.addWatchDir(tmpDir); err != nil {
		t.Fatalf("addWatchDir(%q) error = %v", tmpDir, err)
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

func containsWatch(watches []string, target string) bool {
	for _, watch := range watches {
		if watch == target {
			return true
		}
	}
	return false
}
