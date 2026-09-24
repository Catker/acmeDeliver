package config

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

const testClientConfigContent = `
client:
  server: "http://file-config:1111"
  password: "file-password"
  workdir: "/tmp/file-workdir"
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
		assert.True(t, cfg.Debug)
	})

	t.Run("3. Environment > Config File", func(t *testing.T) {
		configFile := createTempConfig(t, testClientConfigContent)

		t.Setenv("ACMEDELIVER_SERVER", "http://env-config:2222")
		t.Setenv("ACMEDELIVER_PASSWORD", "env-password")
		t.Setenv("ACMEDELIVER_DEBUG", "false")

		cfg, err := LoadClientConfig(configFile)
		assert.NoError(t, err)
		assert.Equal(t, "http://env-config:2222", cfg.Server, "Env server should override file server")
		assert.Equal(t, "env-password", cfg.Password, "Env password should override file password")
		assert.False(t, cfg.Debug, "Env debug should override file debug")
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

// TestClientConfigWatcherHotReload 配置文件变化后回调收到新的 subscribe 与 sites
func TestClientConfigWatcherHotReload(t *testing.T) {
	configFile := createTempConfig(t, `
client:
  password: "test"
  subscribe:
    - "old.example.com"
`)

	var got *ClientConfig
	watcher := NewClientConfigWatcher(configFile, func(newCfg *ClientConfig) { got = newCfg })

	updatedContent := `
client:
  password: "test"
  subscribe:
    - "new.example.com"
    - "api.example.com"
  sites:
    - domain: "new.example.com"
      cert_path: "/new/cert.pem"
      reloadcmd: "echo new"
`
	assert.NoError(t, os.WriteFile(configFile, []byte(updatedContent), 0644))

	// 直接调用 reloadConfig 模拟配置文件变化
	watcher.reloadConfig()

	if got == nil {
		t.Fatal("配置重载后应调用回调")
	}
	assert.Equal(t, []string{"new.example.com", "api.example.com"}, got.Subscribe)
	assert.Equal(t, []SiteDeployConfig{
		{Domain: "new.example.com", CertPath: "/new/cert.pem", ReloadCmd: "echo new"},
	}, got.Sites)
}

// TestServerReloadConfigHotReload 验证服务端配置热重载：只更新白名单/代理字段并触发回调
func TestServerReloadConfigHotReload(t *testing.T) {
	path := createTempConfig(t, "ip_whitelist: \"192.168.1.0/24\"\ntrust_proxy: false\n")

	oldGlobal := GlobalConfig
	oldCallback := reloadCallback
	t.Cleanup(func() {
		GlobalConfig = oldGlobal
		reloadCallback = oldCallback
	})

	// 运行中的活动配置：端口 9090、旧白名单
	GlobalConfig = &Config{Port: "9090", Key: "k", IPWhitelist: "10.0.0.0/8"}

	called := 0
	var got *Config
	reloadCallback = func(c *Config) {
		called++
		got = c
	}

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
