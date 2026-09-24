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
	Port        string `yaml:"port"`
	Bind        string `yaml:"bind"`
	BaseDir     string `yaml:"base_dir"`
	Key         string `yaml:"key"`
	TLS         bool   `yaml:"tls"`
	TLSPort     string `yaml:"tls_port"`
	CertFile    string `yaml:"cert_file"`
	KeyFile     string `yaml:"key_file"`
	IPWhitelist string `yaml:"ip_whitelist"` // IP白名单，逗号分隔（支持热重载）
	TrustProxy  bool   `yaml:"trust_proxy"`  // 是否信任代理头 X-Forwarded-For/X-Real-IP（支持热重载）
	ConfigFile  string `yaml:"-"`            // 配置文件路径
}

var (
	GlobalConfig   *Config
	mu             sync.RWMutex
	reloadCallback func(*Config)
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

	// 1. flag 只能 Parse 一次：先从命令行预取 -c，未指定时检查当前目录的 config.yaml
	cfg.ConfigFile = configFlagValue(os.Args[1:])
	if cfg.ConfigFile == "" {
		if _, err := os.Stat("config.yaml"); err == nil {
			cfg.ConfigFile = "config.yaml"
			slog.Info("检测到当前目录存在 config.yaml，自动加载")
		}
	}

	// 2. 从配置文件加载
	if cfg.ConfigFile != "" {
		if err := loadFromFile(cfg, cfg.ConfigFile); err != nil {
			return fmt.Errorf("加载配置文件失败: %w", err)
		}
		slog.Info("已加载配置文件", "file", cfg.ConfigFile)
	}

	// 3. 环境变量覆盖（优先级高于配置文件）
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

	// 4. 命令行最高优先级：以当前值作为 flag 默认值，未指定的参数保持文件/环境变量的值
	flag.StringVar(&cfg.ConfigFile, "c", cfg.ConfigFile, "配置文件路径")
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

	// 设置密码：空密码时自动生成
	if cfg.Key == "" {
		cfg.Key = uuid.New().String()
		fmt.Printf("自动生成安全密钥，请配置客户端密码: %s\n", cfg.Key)
		fmt.Printf("环境变量: export ACMEDELIVER_KEY=%s\n", cfg.Key)
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

// configFlagValue 从命令行参数中取出 -c/--c 的值（支持 "-c path" 与 "-c=path"）
func configFlagValue(args []string) string {
	for i, arg := range args {
		name, value, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if !strings.HasPrefix(arg, "-") || name != "c" {
			continue
		}
		if hasValue {
			return value
		}
		if i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
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

	// 监听所在目录而非文件本身：编辑器"写临时文件+rename"会替换 inode，直接监听文件会失效
	if err := watcher.Add(filepath.Dir(path)); err != nil {
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
			if isConfigFileEvent(event, path) {
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

// isConfigFileEvent 判断目录监听事件是否为目标配置文件的写入/创建（含 rename 替换）
func isConfigFileEvent(event fsnotify.Event, path string) bool {
	if event.Op&(fsnotify.Write|fsnotify.Create) == 0 {
		return false
	}
	return filepath.Clean(event.Name) == filepath.Clean(path)
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
	callback := reloadCallback
	mu.Unlock()

	slog.Info("✅ 配置文件重载成功",
		"ipWhitelist", newActiveCfg.IPWhitelist,
		"trustProxy", newActiveCfg.TrustProxy)

	if callback != nil {
		callback(&newActiveCfg)
	}
}

// RegisterReloadCallback 设置配置重载回调（仅一个，后设置的覆盖先前的）
func RegisterReloadCallback(callback func(*Config)) {
	mu.Lock()
	reloadCallback = callback
	mu.Unlock()
}

// GetConfig 获取当前配置（线程安全）
func GetConfig() *Config {
	mu.RLock()
	defer mu.RUnlock()
	return GlobalConfig
}

// ClientConfig 客户端配置结构
type ClientConfig struct {
	Server   string `yaml:"server"`
	Password string `yaml:"password"`
	WorkDir  string `yaml:"workdir"`
	Debug    bool   `yaml:"debug"`
	// 全局域名列表，--deploy 未指定 -d 时处理这些域名
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
	ReloadDebounce    int  `yaml:"reload_debounce"`    // Reload 防抖延迟（秒），默认 5 秒
	SyncInterval      int  `yaml:"sync_interval"`      // 定时同步间隔（秒），0/未设置=默认 3600，负数禁用
}

// SiteDeployConfig 站点部署配置
type SiteDeployConfig struct {
	Domain        string `yaml:"domain"`
	CertPath      string `yaml:"cert_path"`
	KeyPath       string `yaml:"key_path"`
	FullchainPath string `yaml:"fullchain_path"`
	ReloadCmd     string `yaml:"reloadcmd"`
}

// FindSiteConfig 按配置顺序返回第一个匹配 domain 的站点配置，未匹配返回 nil。
// 匹配规则：精确匹配，或 "*." 前缀通配配置按域名后缀匹配。
// CLI 与 Daemon 模式共用此实现，保持"配置顺序中第一个命中"的语义。
func FindSiteConfig(sites []SiteDeployConfig, domain string) *SiteDeployConfig {
	for i := range sites {
		site := &sites[i]
		// 精确匹配
		if site.Domain == domain {
			return site
		}
		// 通配符匹配
		if strings.HasPrefix(site.Domain, "*.") {
			suffix := site.Domain[1:] // .example.com
			if strings.HasSuffix(domain, suffix) {
				return site
			}
		}
	}
	return nil
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
		WorkDir:          "/var/lib/acmedeliver",
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

		if fileCfg.Client == nil {
			return nil, fmt.Errorf("配置文件 %s 缺少 client: 根节点（客户端配置示例见 client-config.yaml.example）", configPath)
		}
		cfg = fileCfg.Client
		// 确保有默认值
		if cfg.Server == "" {
			cfg.Server = "http://localhost:9090"
		}
		if cfg.WorkDir == "" {
			cfg.WorkDir = "/var/lib/acmedeliver"
		}
	}

	// 2. 从环境变量覆盖
	cfg.Server = getEnvStr("ACMEDELIVER_SERVER", cfg.Server)
	cfg.Password = getEnvStr("ACMEDELIVER_PASSWORD", cfg.Password)
	cfg.WorkDir = getEnvStr("ACMEDELIVER_WORKDIR", cfg.WorkDir)
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
                    # 开启后取 X-Forwarded-For 最右一项（最近一跳代理追加），无该头时取 X-Real-IP

# 客户端配置请参考 client-config.yaml.example（客户端配置文件根节点为 client:）
`
	return example
}

// ============================================
// 客户端配置热重载
// ============================================

// ClientConfigWatcher 客户端配置监听器（Daemon 模式下热重载 subscribe 与 sites）
type ClientConfigWatcher struct {
	configPath string
	onChange   func(newCfg *ClientConfig)
	stop       chan struct{}
}

// NewClientConfigWatcher 创建客户端配置监听器，配置文件变化且加载成功时调用 onChange
func NewClientConfigWatcher(configPath string, onChange func(newCfg *ClientConfig)) *ClientConfigWatcher {
	return &ClientConfigWatcher{
		configPath: configPath,
		onChange:   onChange,
		stop:       make(chan struct{}),
	}
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

	// 监听所在目录，兼容编辑器"写临时文件+rename"的保存方式
	if err := watcher.Add(filepath.Dir(w.configPath)); err != nil {
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
			if isConfigFileEvent(event, w.configPath) {
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

// reloadConfig 重新加载配置并通知回调（回调只应用 subscribe 与 sites）
func (w *ClientConfigWatcher) reloadConfig() {
	// 不做完整校验：密码可能只通过 -k 传入，回调也只取 subscribe 与 sites
	newCfg, err := LoadClientConfigUnvalidated(w.configPath)
	if err != nil {
		slog.Error("❌ 客户端配置重载失败", "error", err)
		return
	}

	slog.Info("✅ 客户端配置重载成功",
		"subscribe", newCfg.Subscribe,
		"sites", len(newCfg.Sites))
	w.onChange(newCfg)
}
