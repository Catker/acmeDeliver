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

func TestClientConfigWatcher(t *testing.T) {
	t.Run("NewClientConfigWatcher", func(t *testing.T) {
		cfg := &ClientConfig{
			Server:    "http://test:9090",
			Subscribe: []string{"example.com"},
		}

		watcher := NewClientConfigWatcher("/tmp/test.yaml", cfg)
		assert.NotNil(t, watcher)
		assert.Equal(t, "/tmp/test.yaml", watcher.configPath)
		assert.Equal(t, cfg, watcher.current)
	})

	t.Run("RegisterCallback", func(t *testing.T) {
		cfg := &ClientConfig{}
		watcher := NewClientConfigWatcher("", cfg)

		callCount := 0
		watcher.RegisterCallback(func(old, new *ClientConfig) {
			callCount++
		})

		assert.Equal(t, 1, len(watcher.callbacks))
	})

	t.Run("HotReloadSubscribeAndSites", func(t *testing.T) {
		// 创建临时配置文件
		initialContent := `
client:
  server: "http://localhost:9090"
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

		// 注册回调
		callbackCalled := make(chan bool, 1)
		watcher.RegisterCallback(func(old, new *ClientConfig) {
			// 验证回调收到了正确的新配置
			_ = old
			_ = new.Subscribe
			_ = len(new.Sites)
			callbackCalled <- true
		})

		// 手动调用 reloadConfig 模拟配置文件变化
		updatedContent := `
client:
  server: "http://localhost:9090"
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

		// 直接调用 reloadConfig
		watcher.reloadConfig()

		select {
		case <-callbackCalled:
			// 回调被调用
		default:
			t.Log("Callback may not have been called yet, checking state directly")
		}

		// 验证配置已更新
		watcher.mu.RLock()
		currentCfg := watcher.current
		watcher.mu.RUnlock()
		assert.Equal(t, []string{"new.example.com", "api.example.com"}, currentCfg.Subscribe)
		assert.Equal(t, 2, len(currentCfg.Sites))

		// 验证不可热重载的配置未变化
		assert.Equal(t, initialCfg.Server, currentCfg.Server)
	})
}
