package config

import (
	"flag"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

const testClientConfigContent = `
client:
  server: "http://file-config:1111"
  password: "file-password"
  workdir: "/tmp/file-workdir"
  ip_mode: 4
  debug: true
`

const testServerConfigContent = `
port: "7070"
bind: "127.0.0.1"
key: "file-key"
`

func createTempConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	err := os.WriteFile(path, []byte(content), 0644)
	assert.NoError(t, err)
	return path
}

func TestLoadClientConfigPriority(t *testing.T) {
	t.Run("1. Defaults should fail without password", func(t *testing.T) {
		_, err := LoadClientConfig("")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "未配置密码")
	})

	t.Run("2. Config File", func(t *testing.T) {
		configFile := createTempConfig(t, testClientConfigContent)
		cfg, err := LoadClientConfig(configFile)
		assert.NoError(t, err)
		assert.Equal(t, "http://file-config:1111", cfg.Server)
		assert.Equal(t, "file-password", cfg.Password)
		assert.Equal(t, 4, cfg.IPMode)
	})

	t.Run("3. Environment > Config File", func(t *testing.T) {
		configFile := createTempConfig(t, testClientConfigContent)

		t.Setenv("ACMEDELIVER_SERVER", "http://env-config:2222")
		t.Setenv("ACMEDELIVER_PASSWORD", "env-password")
		t.Setenv("ACMEDELIVER_IP_MODE", "6")

		cfg, err := LoadClientConfig(configFile)
		assert.NoError(t, err)
		assert.Equal(t, "http://env-config:2222", cfg.Server, "Env server should override file server")
		assert.Equal(t, "env-password", cfg.Password, "Env password should override file password")
		assert.Equal(t, 6, cfg.IPMode, "Env ip_mode should override file ip_mode")
	})

	t.Run("4. Environment only", func(t *testing.T) {
		t.Setenv("ACMEDELIVER_SERVER", "http://env-only:3333")
		t.Setenv("ACMEDELIVER_PASSWORD", "env-only-password")

		cfg, err := LoadClientConfig("")
		assert.NoError(t, err)
		assert.Equal(t, "http://env-only:3333", cfg.Server)
		assert.Equal(t, "env-only-password", cfg.Password)
	})

	t.Run("5. Relative workdir should fail", func(t *testing.T) {
		relativeWorkdirConfig := `
client:
  password: "test-password"
  workdir: "./relative/path"
`
		configFile := createTempConfig(t, relativeWorkdirConfig)
		_, err := LoadClientConfig(configFile)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "workdir 必须使用绝对路径")
	})
}

// resetFlags 重置全局状态以允许隔离测试
func resetFlags() {
	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ExitOnError)
}

func TestInitServerConfigPriority(t *testing.T) {
	// Helper to run InitConfig with args and cleanup
	runInit := func(args ...string) {
		t.Helper()
		oldArgs := os.Args
		defer func() { os.Args = oldArgs }()
		os.Args = append([]string{"test"}, args...)
		resetFlags()
		InitConfig()
	}

	t.Run("1. Defaults", func(t *testing.T) {
		runInit()
		cfg := GetConfig()
		assert.Equal(t, "9090", cfg.Port)
		assert.Equal(t, "", cfg.Bind)
		// 默认生成 UUID 格式的密钥
		assert.NotEmpty(t, cfg.Key)
		assert.Regexp(t, `[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`, cfg.Key)
	})

	t.Run("2. Config File", func(t *testing.T) {
		configFile := createTempConfig(t, testServerConfigContent)
		runInit("-c", configFile)
		cfg := GetConfig()
		assert.Equal(t, "7070", cfg.Port)
		assert.Equal(t, "127.0.0.1", cfg.Bind)
		assert.Equal(t, "file-key", cfg.Key)
	})

	t.Run("3. Environment > Config File", func(t *testing.T) {
		configFile := createTempConfig(t, testServerConfigContent)

		t.Setenv("ACMEDELIVER_PORT", "8080")
		t.Setenv("ACMEDELIVER_KEY", "env-key")

		runInit("-c", configFile)
		cfg := GetConfig()
		assert.Equal(t, "8080", cfg.Port, "Env port should override file port")
		assert.Equal(t, "env-key", cfg.Key, "Env key should override file key")
	})

	t.Run("4. Command Line > Environment > Config File", func(t *testing.T) {
		configFile := createTempConfig(t, testServerConfigContent)

		// Lowest level: Config file
		// port: "7070"

		// Middle level: Environment variables
		t.Setenv("ACMEDELIVER_PORT", "8080")   // Will be overridden by -p flag
		t.Setenv("ACMEDELIVER_KEY", "env-key") // Will be overridden by -k flag

		// Highest level: Command line flags
		cliPort := "9090"
		cliKey := "cli-key"

		runInit("-c", configFile, "-p", cliPort, "-k", cliKey)
		cfg := GetConfig()

		assert.Equal(t, cliPort, cfg.Port, "CLI flag '-p' should have the highest priority for port")
		assert.Equal(t, cliKey, cfg.Key, "CLI flag '-k' should have the highest priority for key")

		// bind was not specified by flag nor env, so it should take file value
		assert.Equal(t, "127.0.0.1", cfg.Bind, "bind should come from config file")
	})

	t.Run("5. Command Line only", func(t *testing.T) {
		cliPort := "9999"

		runInit("-p", cliPort)
		cfg := GetConfig()

		assert.Equal(t, cliPort, cfg.Port)
		// 默认生成 UUID 格式的密钥
		assert.NotEmpty(t, cfg.Key)
		assert.Regexp(t, `[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`, cfg.Key)
	})

	t.Run("6. Environment only", func(t *testing.T) {
		t.Setenv("ACMEDELIVER_PORT", "8888")

		runInit()
		cfg := GetConfig()

		assert.Equal(t, "8888", cfg.Port)
		// 默认生成 UUID 格式的密钥
		assert.NotEmpty(t, cfg.Key)
		assert.Regexp(t, `[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`, cfg.Key)
	})
}

// TestClientConfigWatcherHotReload 覆盖原 NewClientConfigWatcher / RegisterCallback
// 纯 setter 子测试：构造与注册通过真实重载行为验证（回调各被调用一次）
func TestClientConfigWatcherHotReload(t *testing.T) {
	// 创建临时配置文件
	initialContent := `
client:
  server: "http://old-server:1111"
  password: "test"
  subscribe:
    - "old.example.com"
  sites:
    - domain: "old.example.com"
      cert_path: "/old/cert.pem"
      reloadcmd: "echo old"
`
	configFile := createTempConfig(t, initialContent)

	// 加载初始配置
	initialCfg, err := LoadClientConfig(configFile)
	assert.NoError(t, err)
	assert.Equal(t, []string{"old.example.com"}, initialCfg.Subscribe)

	// 创建 watcher
	watcher := NewClientConfigWatcher(configFile, initialCfg)
	assert.NotNil(t, watcher)

	// 注册两个回调，记录调用次数与收到的配置
	var mu sync.Mutex
	callCounts := []int{0, 0}
	var gotOlds [2]*ClientConfig
	var gotNews [2]*ClientConfig
	watcher.RegisterCallback(func(oldCfg, newCfg *ClientConfig) {
		mu.Lock()
		defer mu.Unlock()
		callCounts[0]++
		gotOlds[0], gotNews[0] = oldCfg, newCfg
	})
	watcher.RegisterCallback(func(oldCfg, newCfg *ClientConfig) {
		mu.Lock()
		defer mu.Unlock()
		callCounts[1]++
		gotOlds[1], gotNews[1] = oldCfg, newCfg
	})

	// 更新配置：Server 不同（不可热重载字段），subscribe 与 sites 均变化
	updatedContent := `
client:
  server: "http://new-server:2222"
  password: "test"
  subscribe:
    - "new.example.com"
    - "api.example.com"
  sites:
    - domain: "new.example.com"
      cert_path: "/new/cert.pem"
      reloadcmd: "echo new"
    - domain: "api.example.com"
      cert_path: "/api/cert.pem"
      reloadcmd: "echo api"
`
	err = os.WriteFile(configFile, []byte(updatedContent), 0644)
	assert.NoError(t, err)

	// 直接调用 reloadConfig 模拟配置文件变化
	watcher.reloadConfig()

	wantSites := []SiteDeployConfig{
		{Domain: "new.example.com", CertPath: "/new/cert.pem", ReloadCmd: "echo new"},
		{Domain: "api.example.com", CertPath: "/api/cert.pem", ReloadCmd: "echo api"},
	}

	mu.Lock()
	// 两个注册的回调都各执行且只执行一次
	assert.Equal(t, []int{1, 1}, callCounts, "每个注册的回调都应被调用一次")

	// 回调收到重载前的旧配置
	for i, oldCfg := range gotOlds {
		if oldCfg == nil {
			t.Fatalf("回调 %d 未收到旧配置", i)
		}
		assert.Equal(t, []string{"old.example.com"}, oldCfg.Subscribe)
		assert.Len(t, oldCfg.Sites, 1)
		assert.Equal(t, "old.example.com", oldCfg.Sites[0].Domain)
	}

	// 回调收到新订阅与站点内容；Server 属于不可热重载字段，保持旧值
	for i, newCfg := range gotNews {
		if newCfg == nil {
			t.Fatalf("回调 %d 未收到新配置", i)
		}
		assert.Equal(t, []string{"new.example.com", "api.example.com"}, newCfg.Subscribe)
		assert.Equal(t, wantSites, newCfg.Sites)
		assert.Equal(t, "http://old-server:1111", newCfg.Server,
			"Server 不可热重载，应保持旧值")
	}
	mu.Unlock()

	// watcher 当前状态已更新为新的订阅与站点
	watcher.mu.RLock()
	currentCfg := watcher.current
	watcher.mu.RUnlock()
	assert.Equal(t, []string{"new.example.com", "api.example.com"}, currentCfg.Subscribe)
	assert.Equal(t, wantSites, currentCfg.Sites)
	assert.Equal(t, "http://old-server:1111", currentCfg.Server,
		"Server 不可热重载，应保持旧值")
}

// TestServerReloadConfigHotReload 验证服务端配置热重载：只更新白名单/代理字段并触发回调
func TestServerReloadConfigHotReload(t *testing.T) {
	path := createTempConfig(t, "ip_whitelist: \"192.168.1.0/24\"\ntrust_proxy: false\n")

	oldGlobal := GlobalConfig
	oldCallbacks := reloadCallbacks
	t.Cleanup(func() {
		GlobalConfig = oldGlobal
		reloadCallbacks = oldCallbacks
	})

	// 运行中的活动配置：端口 9090、旧白名单
	GlobalConfig = &Config{Port: "9090", Key: "k", IPWhitelist: "10.0.0.0/8"}

	called := 0
	var got *Config
	reloadCallbacks = []func(*Config){func(c *Config) {
		called++
		got = c
	}}

	// 配置文件更新：白名单与 trust_proxy 变化，port 也写了新值但不可热重载
	updated := "port: \"9999\"\nip_whitelist: \"127.0.0.1\"\ntrust_proxy: true\n"
	assert.NoError(t, os.WriteFile(path, []byte(updated), 0644))

	reloadConfig(path)

	assert.Equal(t, 1, called, "重载回调应被调用一次")
	assert.NotNil(t, got)
	assert.Equal(t, "127.0.0.1", got.IPWhitelist)
	assert.True(t, got.TrustProxy)
	assert.Equal(t, "9090", got.Port, "Port 不可热重载，回调收到的活动配置应保持原值")

	active := GetConfig()
	assert.Equal(t, "127.0.0.1", active.IPWhitelist)
	assert.True(t, active.TrustProxy)
	assert.Equal(t, "9090", active.Port, "Port 不可热重载，活动配置应保持原值")
}

func TestFindSiteConfig(t *testing.T) {
	sites := []SiteDeployConfig{
		{Domain: "*.example.com", CertPath: "/wild/{domain}/cert.pem"},
		{Domain: "api.example.com", CertPath: "/api/cert.pem"},
		{Domain: "*.other.org", CertPath: "/other/{domain}/cert.pem"},
	}

	tests := []struct {
		name   string
		domain string
		want   string // 期望命中的 CertPath；"-" 表示不匹配
	}{
		// 配置顺序优先：靠前的通配先于靠后的精确命中
		{name: "通配在前则通配胜出", domain: "api.example.com", want: "/wild/{domain}/cert.pem"},
		{name: "通配子域名命中", domain: "www.example.com", want: "/wild/{domain}/cert.pem"},
		{name: "多级子域名命中", domain: "a.b.example.com", want: "/wild/{domain}/cert.pem"},
		{name: "第二个通配命中", domain: "x.other.org", want: "/other/{domain}/cert.pem"},
		// 裸域不带子域前缀，不匹配通配
		{name: "裸域不匹配通配", domain: "example.com", want: "-"},
		{name: "后缀但非子域不匹配", domain: "notexample.com", want: "-"},
		{name: "完全不同域名", domain: "unrelated.net", want: "-"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FindSiteConfig(sites, tt.domain)
			if tt.want == "-" {
				if got != nil {
					t.Errorf("FindSiteConfig(%q) = %+v, want nil", tt.domain, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("FindSiteConfig(%q) = nil, want CertPath %q", tt.domain, tt.want)
			}
			if got.CertPath != tt.want {
				t.Errorf("FindSiteConfig(%q) 命中 CertPath = %q, want %q", tt.domain, got.CertPath, tt.want)
			}
		})
	}

	t.Run("精确优先于配置靠后的场景由顺序决定", func(t *testing.T) {
		ordered := []SiteDeployConfig{
			{Domain: "exact.com", CertPath: "/exact/cert.pem"},
			{Domain: "*.exact.com", CertPath: "/wild/cert.pem"},
		}
		got := FindSiteConfig(ordered, "exact.com")
		if got == nil || got.CertPath != "/exact/cert.pem" {
			t.Errorf("精确在前应命中精确配置，得到 %+v", got)
		}
	})

	t.Run("空站点列表", func(t *testing.T) {
		if got := FindSiteConfig(nil, "example.com"); got != nil {
			t.Errorf("FindSiteConfig(nil, _) = %+v, want nil", got)
		}
	})
}
