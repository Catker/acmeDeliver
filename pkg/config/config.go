package config

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

// 环境变量辅助函数
func getEnvStr(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if value, exists := os.LookupEnv(key); exists {
		if i, err := strconv.Atoi(value); err == nil {
			return i
		}
	}
	return fallback
}

func getEnvBool(key string, fallback bool) bool {
	if value, exists := os.LookupEnv(key); exists {
		if b, err := strconv.ParseBool(value); err == nil {
			return b
		}
	}
	return fallback
}

// Config 配置结构
type Config struct {
	Port        string        `yaml:"port"`
	Bind        string        `yaml:"bind"`
	BaseDir     string        `yaml:"base_dir"`
	Key         string        `yaml:"key"`
	TLS         bool          `yaml:"tls"`
	TLSPort     string        `yaml:"tls_port"`
	CertFile    string        `yaml:"cert_file"`
	KeyFile     string        `yaml:"key_file"`
	IPWhitelist string        `yaml:"ip_whitelist"`     // IP白名单，逗号分隔（支持热重载）
	TrustProxy  bool          `yaml:"trust_proxy"`      // 是否信任代理头 X-Forwarded-For/X-Real-IP（支持热重载）
	ConfigFile  string        `yaml:"-"`                // 配置文件路径
	Client      *ClientConfig `yaml:"client,omitempty"` // 客户端配置（可选）
}

var (
	GlobalConfig    *Config
	mu              sync.RWMutex
	reloadCallbacks []func(*Config)
)

// InitConfig 初始化服务端配置
// 优先级：命令行 > 环境变量 > 配置文件 > 默认值
// 返回错误时调用方应自行处理（如 os.Exit）
func InitConfig() error {
	cfg := &Config{
		// 默认值
		Port:     "9090",
		Bind:     "",
		BaseDir:  "./",
		Key:      "",
		TLS:      false,
		TLSPort:  "9443",
		CertFile: "cert.pem",
		KeyFile:  "key.pem",
	}

	// 1. 先解析 -c 参数以获取配置文件路径
	flag.StringVar(&cfg.ConfigFile, "c", "", "配置文件路径")
	flag.StringVar(&cfg.Bind, "b", cfg.Bind, "绑定监听地址")
	flag.StringVar(&cfg.Port, "p", cfg.Port, "服务端口")
	flag.StringVar(&cfg.BaseDir, "d", cfg.BaseDir, "证书文件所在目录")
	flag.StringVar(&cfg.Key, "k", cfg.Key, "密码")
	flag.BoolVar(&cfg.TLS, "tls", cfg.TLS, "是否启用TLS")
	flag.StringVar(&cfg.TLSPort, "tlsport", cfg.TLSPort, "TLS端口")
	flag.StringVar(&cfg.CertFile, "cert", cfg.CertFile, "TLS证书文件")
	flag.StringVar(&cfg.KeyFile, "key", cfg.KeyFile, "TLS私钥文件")
	flag.StringVar(&cfg.IPWhitelist, "whitelist", cfg.IPWhitelist, "IP白名单（逗号分隔，支持CIDR）")
	flag.Parse()

	// 命令行参数暂存
	cliArgs := make(map[string]string)
	flag.Visit(func(f *flag.Flag) {
		cliArgs[f.Name] = f.Value.String()
	})

	// 2. 如果未指定配置文件，检查当前目录是否存在 config.yaml
	if cfg.ConfigFile == "" {
		if _, err := os.Stat("config.yaml"); err == nil {
			cfg.ConfigFile = "config.yaml"
			slog.Info("检测到当前目录存在 config.yaml，自动加载")
		}
	}

	// 3. 从配置文件加载（如果指定或自动检测到）
	if cfg.ConfigFile != "" {
		if err := loadFromFile(cfg, cfg.ConfigFile); err != nil {
			return fmt.Errorf("加载配置文件失败: %w", err)
		}
		slog.Info("已加载配置文件", "file", cfg.ConfigFile)
	}

	// 3. 从环境变量覆盖（优先级高于配置文件）
	cfg.Port = getEnvStr("ACMEDELIVER_PORT", cfg.Port)
	cfg.Bind = getEnvStr("ACMEDELIVER_BIND", cfg.Bind)
	cfg.BaseDir = getEnvStr("ACMEDELIVER_BASE_DIR", cfg.BaseDir)
	cfg.Key = getEnvStr("ACMEDELIVER_KEY", cfg.Key)
	cfg.TLS = getEnvBool("ACMEDELIVER_TLS", cfg.TLS)
	cfg.TLSPort = getEnvStr("ACMEDELIVER_TLS_PORT", cfg.TLSPort)
	cfg.CertFile = getEnvStr("ACMEDELIVER_CERT_FILE", cfg.CertFile)
	cfg.KeyFile = getEnvStr("ACMEDELIVER_KEY_FILE", cfg.KeyFile)
	cfg.IPWhitelist = getEnvStr("ACMEDELIVER_IP_WHITELIST", cfg.IPWhitelist)
	cfg.TrustProxy = getEnvBool("ACMEDELIVER_TRUST_PROXY", cfg.TrustProxy)

	// 4. 命令行参数再次覆盖（最高优先级）
	for name, value := range cliArgs {
		switch name {
		case "b":
			cfg.Bind = value
		case "p":
			cfg.Port = value
		case "d":
			cfg.BaseDir = value
		case "k":
			cfg.Key = value
		case "tls":
			if v, err := strconv.ParseBool(value); err == nil {
				cfg.TLS = v
			}
		case "tlsport":
			cfg.TLSPort = value
		case "cert":
			cfg.CertFile = value
		case "key":
			cfg.KeyFile = value
		case "whitelist":
			cfg.IPWhitelist = value
		}
	}

	// 设置密码：空密码时自动生成
	if cfg.Key == "" {
		cfg.Key = GenerateSecureKey()
		fmt.Printf("\n╔════════════════════════════════════════════════════════════╗\n")
		fmt.Printf("║  🔐 自动生成安全密钥                                        ║\n")
		fmt.Printf("║                                                            ║\n")
		fmt.Printf("║  请配置客户端密码: %s ║\n", cfg.Key)
		fmt.Printf("║                                                            ║\n")
		fmt.Printf("║  环境变量: export ACMEDELIVER_KEY=%s ║\n", cfg.Key[:16]+"...")
		fmt.Printf("╚════════════════════════════════════════════════════════════╝\n\n")
		slog.Info("自动生成安全密钥", "key_preview", cfg.Key[:8]+"...")
	}

	mu.Lock()
	GlobalConfig = cfg
	mu.Unlock()

	// 启动配置文件监听（如果指定了配置文件）
	if cfg.ConfigFile != "" {
		go watchConfig(cfg.ConfigFile)
	}

	slog.Info("配置已加载", "port", cfg.Port, "baseDir", cfg.BaseDir)
	return nil
}

// loadFromFile 从文件加载配置
func loadFromFile(cfg *Config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	return yaml.Unmarshal(data, cfg)
}

// watchConfig 监听配置文件变化
func watchConfig(path string) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		slog.Warn("创建文件监听器失败", "error", err)
		return
	}
	defer watcher.Close()

	if err := watcher.Add(path); err != nil {
		slog.Warn("监听配置文件失败", "error", err)
		return
	}

	slog.Info("🔄 配置文件热重载已启用", "path", path)

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Op&fsnotify.Write == fsnotify.Write {
				slog.Info("📝 检测到配置文件变化，正在重新加载...", "file", event.Name)
				reloadConfig(path)
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			slog.Warn("文件监听错误", "error", err)
		}
	}
}

// reloadConfig 重新加载配置
func reloadConfig(path string) {
	newCfgFromFile := &Config{}
	if err := loadFromFile(newCfgFromFile, path); err != nil {
		slog.Error("❌ 配置文件重载失败", "error", err)
		return
	}

	mu.Lock()
	// 创建一个新配置的副本，以保留不可热重载的字段
	newActiveCfg := *GlobalConfig

	// 只更新支持热重载的配置项
	newActiveCfg.IPWhitelist = newCfgFromFile.IPWhitelist
	newActiveCfg.TrustProxy = newCfgFromFile.TrustProxy
	GlobalConfig = &newActiveCfg
	mu.Unlock()

	slog.Info("✅ 配置文件重载成功",
		"ipWhitelist", newActiveCfg.IPWhitelist,
		"trustProxy", newActiveCfg.TrustProxy)

	// 调用回调函数
	for _, callback := range reloadCallbacks {
		callback(&newActiveCfg)
	}
}

// RegisterReloadCallback 注册配置重载回调
func RegisterReloadCallback(callback func(*Config)) {
	reloadCallbacks = append(reloadCallbacks, callback)
}

// GetConfig 获取当前配置（线程安全）
func GetConfig() *Config {
	mu.RLock()
	defer mu.RUnlock()
	return GlobalConfig
}

// GenerateSecureKey 生成安全的随机密钥
func GenerateSecureKey() string {
	return uuid.New().String()
}

// ClientConfig 客户端配置结构
type ClientConfig struct {
	Server   string `yaml:"server"`
	Password string `yaml:"password"`
	WorkDir  string `yaml:"workdir"`
	IPMode   int    `yaml:"ip_mode"` // 0=默认, 4=IPv4, 6=IPv6
	Debug    bool   `yaml:"debug"`
	// 全局域名列表，用于 --list 和无参数时处理所有域名
	Domains []string `yaml:"domains,omitempty"`
	// 默认的重载/重启服务命令
	DefaultReloadCmd string `yaml:"default_reload_cmd,omitempty"`

	// TLS 配置（用于自签证书场景）
	TLSCaFile             string `yaml:"tls_ca_file"`              // 信任的 CA 证书路径
	TLSInsecureSkipVerify bool   `yaml:"tls_insecure_skip_verify"` // 跳过证书验证（仅开发用）

	// Daemon 模式配置
	Daemon DaemonModeConfig `yaml:"daemon,omitempty"`
	// 订阅的域名列表（Daemon 模式使用，Pull 模式使用 Domains 或 -d 参数）
	Subscribe []string `yaml:"subscribe,omitempty"`
	// 站点部署配置（CLI 和 Daemon 模式共用）
	Sites []SiteDeployConfig `yaml:"sites,omitempty"`
}

// DaemonModeConfig Daemon 模式配置
type DaemonModeConfig struct {
	Enabled           bool `yaml:"enabled"`
	ReconnectInterval int  `yaml:"reconnect_interval"` // 重连间隔（秒）
	HeartbeatInterval int  `yaml:"heartbeat_interval"` // 心跳间隔（秒）
	ReloadDebounce    int  `yaml:"reload_debounce"`    // Reload 防抖延迟（秒），默认 5 秒
	SyncInterval      int  `yaml:"sync_interval"`      // 定时同步间隔（秒），0 禁用，默认 3600（1小时）
}

// SiteDeployConfig 站点部署配置
type SiteDeployConfig struct {
	Domain        string `yaml:"domain"`
	CertPath      string `yaml:"cert_path"`
	KeyPath       string `yaml:"key_path"`
	FullchainPath string `yaml:"fullchain_path"`
	ReloadCmd     string `yaml:"reloadcmd"`
}

// ClientConfigFile 客户端配置文件结构（用于 YAML 解析）
type ClientConfigFile struct {
	Client *ClientConfig `yaml:"client"`
}

// LoadClientConfigUnvalidated 加载客户端配置但不做最终校验
// 优先级：环境变量 > 配置文件 > 默认值
// 命令行参数由调用方自行覆盖
func LoadClientConfigUnvalidated(configPath string) (*ClientConfig, error) {
	cfg := &ClientConfig{
		// 默认值
		Server:           "http://localhost:9090",
		Password:         "", // 空密码，允许命令行后续覆盖
		WorkDir:          "/tmp/acme",
		IPMode:           0,
		Debug:            false,
		Domains:          []string{},
		DefaultReloadCmd: "",
	}

	// 1. 从配置文件加载
	if configPath != "" {
		data, err := os.ReadFile(configPath)
		if err != nil {
			return nil, err
		}

		// 尝试解析为客户端配置文件格式
		var fileCfg ClientConfigFile
		if err := yaml.Unmarshal(data, &fileCfg); err != nil {
			return nil, err
		}

		if fileCfg.Client != nil {
			cfg = fileCfg.Client
			// 确保有默认值
			if cfg.Server == "" {
				cfg.Server = "http://localhost:9090"
			}
			if cfg.WorkDir == "" {
				cfg.WorkDir = "/tmp/acme"
			}
		}
	}

	// 2. 从环境变量覆盖
	cfg.Server = getEnvStr("ACMEDELIVER_SERVER", cfg.Server)
	cfg.Password = getEnvStr("ACMEDELIVER_PASSWORD", cfg.Password)
	cfg.WorkDir = getEnvStr("ACMEDELIVER_WORKDIR", cfg.WorkDir)
	cfg.IPMode = getEnvInt("ACMEDELIVER_IP_MODE", cfg.IPMode)
	cfg.Debug = getEnvBool("ACMEDELIVER_DEBUG", cfg.Debug)

	// TLS 配置环境变量
	cfg.TLSCaFile = getEnvStr("ACMEDELIVER_TLS_CA_FILE", cfg.TLSCaFile)
	cfg.TLSInsecureSkipVerify = getEnvBool("ACMEDELIVER_TLS_INSECURE_SKIP_VERIFY", cfg.TLSInsecureSkipVerify)

	// 新增：环境变量支持
	cfg.DefaultReloadCmd = getEnvStr("ACMEDELIVER_DEFAULT_RELOAD_CMD", cfg.DefaultReloadCmd)

	// 支持从环境变量读取域名列表（逗号分隔）
	if domainsEnv := getEnvStr("ACMEDELIVER_DOMAINS", ""); domainsEnv != "" {
		domainsList := strings.Split(domainsEnv, ",")
		for i, domain := range domainsList {
			domainsList[i] = strings.TrimSpace(domain)
		}
		cfg.Domains = domainsList
	}

	return cfg, nil
}

// ValidateClientConfig 校验客户端配置合法性
func ValidateClientConfig(cfg *ClientConfig) error {
	// 校验密码必须设置
	if cfg.Password == "" {
		return fmt.Errorf("未配置密码，请设置:\n  • 配置文件: client.password\n  • 环境变量: export ACMEDELIVER_PASSWORD=your-password\n  • 命令行参数: -k your-password")
	}

	// 校验 WorkDir 必须为绝对路径（lockfile 库要求）
	if cfg.WorkDir != "" && !filepath.IsAbs(cfg.WorkDir) {
		return fmt.Errorf("workdir 必须使用绝对路径，当前值: %q（lockfile 库要求）", cfg.WorkDir)
	}

	return nil
}

// LoadClientConfig 加载并校验客户端配置
func LoadClientConfig(configPath string) (*ClientConfig, error) {
	cfg, err := LoadClientConfigUnvalidated(configPath)
	if err != nil {
		return nil, err
	}

	if err := ValidateClientConfig(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

// GenerateExampleConfig 生成示例配置文件
func GenerateExampleConfig() string {
	example := `# acmeDeliver 配置文件
# 基础配置
port: "9090"
bind: ""  # 留空表示绑定所有接口
base_dir: "./"
key: "your-strong-password-here"

# TLS 配置
tls: false
tls_port: "9443"
cert_file: "cert.pem"
key_file: "key.pem"

# 安全配置（支持热重载）
ip_whitelist: ""  # 示例: "192.168.1.0/24,10.0.0.50,127.0.0.1,::1"
                  # ⚠️ 本地测试时记得添加 ::1（IPv6 环回地址）
trust_proxy: false  # 是否信任反向代理头 (X-Forwarded-For, X-Real-IP)
                    # ⚠️ 仅当服务部署在可信反向代理（如 Nginx、Caddy）后面时才设为 true
                    # ⚠️ 直接暴露公网时必须为 false，否则攻击者可伪造 IP 绕过白名单

# 注：状态查询功能现已通过 WebSocket 实现，使用 acmedeliver-client --status 命令

# 客户端配置（可选）
client:
  server: "http://localhost:9090"
  password: "your-strong-password-here"
  workdir: "/tmp/acme"  # 必须使用绝对路径
  ip_mode: 0  # 0=默认, 4=IPv4, 6=IPv6
  debug: false

  # ========== TLS 配置（自签证书场景） ==========
  # 当服务端使用自签证书时，客户端需要指定信任的 CA 证书
  # tls_ca_file: "/path/to/ca.crt"              # 信任的 CA 证书路径
  # tls_insecure_skip_verify: false             # 跳过证书验证（仅开发用，生产环境禁用）

  # (可选) 全局管理的域名列表
  # Pull 模式：用于 --list 命令和无 -d 参数时处理所有域名
  domains:
    - "example.com"
    - "www.example.com"

  # (可选) 部署后执行的默认重载命令
  default_reload_cmd: "systemctl reload nginx"

  # ========== Daemon 模式配置（WebSocket 推送） ==========
  daemon:
    enabled: false              # 是否启用 daemon 模式
    reconnect_interval: 30      # WebSocket 断线重连间隔（秒）
    heartbeat_interval: 60      # 心跳检测间隔（秒）

  # daemon 模式下订阅的域名列表
  subscribe:
    - "example.com"
    - "api.example.com"

  # ========== 站点部署配置（CLI 和 Daemon 共用） ==========
  # 支持为不同域名配置不同的证书路径和重载命令
  # 路径支持 {domain} 占位符，自动替换为实际域名
  sites:
    # 使用 {domain} 占位符（推荐）
    - domain: "*.example.com"
      cert_path: "/etc/nginx/ssl/{domain}/cert.pem"
      key_path: "/etc/nginx/ssl/{domain}/key.pem"
      fullchain_path: "/etc/nginx/ssl/{domain}/fullchain.pem"
      reloadcmd: "systemctl reload nginx"

    # 精确匹配特定域名
    - domain: "api.example.com"
      cert_path: "/etc/apache2/ssl/api/cert.pem"
      key_path: "/etc/apache2/ssl/api/key.pem"
      fullchain_path: "/etc/apache2/ssl/api/fullchain.pem"
      reloadcmd: "systemctl reload apache2"
`
	return example
}

// ============================================
// 客户端配置热重载
// ============================================

// ClientConfigWatcher 客户端配置监听器
type ClientConfigWatcher struct {
	configPath string
	current    *ClientConfig
	callbacks  []func(*ClientConfig, *ClientConfig) // (oldConfig, newConfig)
	mu         sync.RWMutex
	stop       chan struct{}
}

// NewClientConfigWatcher 创建客户端配置监听器
func NewClientConfigWatcher(configPath string, initialConfig *ClientConfig) *ClientConfigWatcher {
	return &ClientConfigWatcher{
		configPath: configPath,
		current:    initialConfig,
		callbacks:  make([]func(*ClientConfig, *ClientConfig), 0),
		stop:       make(chan struct{}),
	}
}

// RegisterCallback 注册配置重载回调
// 回调函数接收 (旧配置, 新配置) 两个参数
func (w *ClientConfigWatcher) RegisterCallback(cb func(*ClientConfig, *ClientConfig)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.callbacks = append(w.callbacks, cb)
}

// Start 启动配置文件监听
func (w *ClientConfigWatcher) Start() error {
	if w.configPath == "" {
		return nil
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}

	if err := watcher.Add(w.configPath); err != nil {
		watcher.Close()
		return err
	}

	go w.watchLoop(watcher)
	return nil
}

// Stop 停止配置监听
func (w *ClientConfigWatcher) Stop() {
	close(w.stop)
}

// watchLoop 监听循环
func (w *ClientConfigWatcher) watchLoop(watcher *fsnotify.Watcher) {
	defer watcher.Close()

	slog.Info("🔄 客户端配置热重载已启用", "path", w.configPath)

	for {
		select {
		case <-w.stop:
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Op&fsnotify.Write == fsnotify.Write {
				slog.Info("📝 检测到客户端配置文件变化，正在重新加载...")
				w.reloadConfig()
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			slog.Warn("客户端配置文件监听错误", "error", err)
		}
	}
}

// reloadConfig 重新加载配置
func (w *ClientConfigWatcher) reloadConfig() {
	newCfg, err := LoadClientConfig(w.configPath)
	if err != nil {
		slog.Error("❌ 客户端配置重载失败", "error", err)
		return
	}

	w.mu.Lock()
	oldCfg := w.current

	// 只更新支持热重载的配置项
	updatedCfg := *oldCfg

	// 热重载: subscribe 订阅列表
	updatedCfg.Subscribe = newCfg.Subscribe

	// 热重载: sites 站点配置
	updatedCfg.Sites = newCfg.Sites

	// 热重载: daemon.heartbeat_interval
	if newCfg.Daemon.HeartbeatInterval > 0 {
		updatedCfg.Daemon.HeartbeatInterval = newCfg.Daemon.HeartbeatInterval
	}

	// 热重载: daemon.reconnect_interval
	if newCfg.Daemon.ReconnectInterval > 0 {
		updatedCfg.Daemon.ReconnectInterval = newCfg.Daemon.ReconnectInterval
	}

	w.current = &updatedCfg
	callbacks := make([]func(*ClientConfig, *ClientConfig), len(w.callbacks))
	copy(callbacks, w.callbacks)
	w.mu.Unlock()

	slog.Info("✅ 客户端配置重载成功",
		"subscribe", updatedCfg.Subscribe,
		"sites", len(updatedCfg.Sites))

	// 调用回调函数
	for _, callback := range callbacks {
		callback(oldCfg, &updatedCfg)
	}
}
