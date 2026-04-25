package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"log/slog"

	"github.com/Catker/acmeDeliver/pkg/client"
	"github.com/Catker/acmeDeliver/pkg/command"
	"github.com/Catker/acmeDeliver/pkg/config"
	"github.com/Catker/acmeDeliver/pkg/deployer"
	"github.com/Catker/acmeDeliver/pkg/workspace"
)

const VERSION = "3.1.1"

// CliOptions 封装所有命令行参数
type CliOptions struct {
	// 基础参数
	Server     string
	Password   string
	DomainsStr string // -d "dom1,dom2" 域名列表
	Debug      bool

	// 功能参数
	Deploy bool // 部署模式：检查更新并部署证书
	Status bool // 查询服务器运行状态（在线客户端 + 证书状态）

	// 网络参数
	IPMode4 bool
	IPMode6 bool

	// 功能增强
	ReloadCmd string // 自定义重载命令
	DryRun    bool   // Dry-Run 模式
	Force     bool   // 强制更新模式

	// Daemon 模式
	Daemon bool // 守护进程模式
}

// parseFlags 解析命令行参数并返回 CliOptions
func parseFlags() *CliOptions {
	opts := &CliOptions{}

	// 基础参数
	flag.StringVar(&configFile, "c", "", "配置文件路径")
	flag.StringVar(&opts.Server, "s", "", "服务器地址")
	flag.StringVar(&opts.Password, "k", "", "认证密码")
	flag.StringVar(&opts.DomainsStr, "d", "", "要操作的域名，多个域名以逗号分隔 (例如 \"d1.com,d2.com\")")
	flag.BoolVar(&opts.Debug, "debug", false, "调试模式")

	// 功能参数
	flag.BoolVar(&opts.Deploy, "deploy", false, "检查更新并部署证书（根据配置文件中的路径部署）")
	flag.BoolVar(&opts.Status, "status", false, "查询服务器运行状态（在线客户端 + 证书状态）")

	// 功能增强参数
	flag.StringVar(&opts.ReloadCmd, "reload-cmd", "", "覆盖默认的重载命令 (例如 \"systemctl reload apache2\")")
	flag.BoolVar(&opts.DryRun, "dry-run", false, "演练模式，只显示将执行的操作，不实际执行")
	flag.BoolVar(&opts.Force, "f", false, "强制更新证书，即使证书尚未过期")

	// 网络参数
	flag.BoolVar(&opts.IPMode4, "4", false, "仅使用IPv4")
	flag.BoolVar(&opts.IPMode6, "6", false, "仅使用IPv6")

	// Daemon 模式
	flag.BoolVar(&opts.Daemon, "daemon", false, "以守护进程模式运行，监听证书推送")

	flag.Usage = usage
	flag.Parse()

	return opts
}

var configFile string

func main() {
	// 1. 解析命令行参数
	opts := parseFlags()

	// 2. 设置日志
	setupLogger(opts.Debug)
	slog.Info("acmeDeliver 客户端启动", "version", VERSION)

	// 3. 加载配置
	cfg := loadConfiguration(opts)

	// 4. 检查是否是 daemon 模式
	// 注意：--status 和 --deploy 是一次性命令，应优先执行，不受 daemon.enabled 配置影响
	if (opts.Daemon || cfg.Daemon.Enabled) && !opts.Status && !opts.Deploy {
		runDaemon(cfg)
		return
	}

	// 5. 验证参数（非 daemon 模式）
	if err := validateArgs(opts); err != nil {
		slog.Error("参数验证失败", "error", err)
		os.Exit(1)
	}

	// 6. 创建 WebSocket 客户端
	tlsConfig := &client.TLSConfig{
		CaFile:             cfg.TLSCaFile,
		InsecureSkipVerify: cfg.TLSInsecureSkipVerify,
	}
	wsClient := client.NewWSClient(cfg.Server, cfg.Password, tlsConfig)
	ctx := context.Background()

	// 连接服务器
	if err := wsClient.Connect(ctx); err != nil {
		slog.Error("连接服务器失败", "error", err)
		os.Exit(1)
	}
	defer wsClient.Close()

	// 7. 运行 CLI 逻辑
	if err := runCLI(ctx, wsClient, cfg, opts); err != nil {
		slog.Error("执行失败", "error", err)
		os.Exit(1)
	}

	slog.Info("操作完成")
}

// runCLI 运行 CLI 业务逻辑
func runCLI(ctx context.Context, wsClient *client.WSClient, cfg *config.ClientConfig, opts *CliOptions) error {

	// 服务器状态查询模式
	if opts.Status {
		status, err := wsClient.GetServerStatus(ctx)
		if err != nil {
			return fmt.Errorf("获取服务器状态失败: %w", err)
		}

		fmt.Println("======== acmeDeliver 服务器状态 ========")
		fmt.Printf("服务器: %s\n", cfg.Server)
		fmt.Printf("生成时间: %s\n\n", time.Unix(status.GeneratedAt, 0).Format("2006-01-02 15:04:05"))

		// 在线客户端
		fmt.Println("─────── 在线客户端 ───────")
		if len(status.Clients) == 0 {
			fmt.Println("当前没有客户端在线")
		} else {
			fmt.Printf("共 %d 个客户端在线:\n\n", len(status.Clients))
			for i, c := range status.Clients {
				connectedAt := time.Unix(c.ConnectedAt, 0)
				duration := time.Since(connectedAt)
				durationStr := formatDuration(duration)
				fmt.Printf("[%d] %s\n", i+1, c.ID)
				fmt.Printf("    IP: %s\n", c.RemoteIP)
				fmt.Printf("    连接时间: %s (已连接 %s)\n", connectedAt.Format("2006-01-02 15:04:05"), durationStr)
				if len(c.Domains) > 0 {
					fmt.Printf("    订阅域名: %s\n", strings.Join(c.Domains, ", "))
				} else {
					fmt.Println("    订阅域名: (无)")
				}
				fmt.Println()
			}
		}

		// 证书状态
		fmt.Println("─────── 证书状态 ───────")
		if len(status.Domains) == 0 {
			fmt.Println("没有可用的域名证书")
		} else {
			fmt.Printf("共 %d 个域名:\n\n", len(status.Domains))
			for i, d := range status.Domains {
				// 状态标记
				statusIcon := "❓"
				statusText := "未知"
				if d.Valid {
					if d.NotAfter > 0 && d.DaysRemaining <= 0 {
						statusIcon = "🔴"
						statusText = "证书已过期"
					} else if d.NotAfter > 0 && d.DaysRemaining <= 7 {
						statusIcon = "🟡"
						statusText = "即将过期"
					} else if d.LastUpdate > 0 {
						statusIcon = "✅"
						statusText = "可用"
					} else {
						statusIcon = "✅"
						statusText = "可用（无时间戳）"
					}
				} else if d.Error != "" {
					statusIcon = "❌"
					statusText = d.Error
				} else {
					statusIcon = "⚠️"
					statusText = "文件异常"
				}

				fmt.Printf("[%d] %s\n", i+1, d.Domain)
				fmt.Printf("    状态: %s %s\n", statusIcon, statusText)

				if d.LastUpdate > 0 {
					tm := time.Unix(d.LastUpdate, 0)
					fmt.Printf("    下发: %s\n", tm.Format("2006-01-02 15:04:05"))
				}

				if d.NotAfter > 0 {
					expireTime := time.Unix(d.NotAfter, 0)
					expiryIcon := "🟢"
					expiryText := fmt.Sprintf("剩余 %d 天", d.DaysRemaining)
					if d.DaysRemaining <= 0 {
						expiryIcon = "🔴"
						expiryText = fmt.Sprintf("已过期 %d 天", -d.DaysRemaining)
					} else if d.DaysRemaining <= 7 {
						expiryIcon = "🔴"
					} else if d.DaysRemaining <= 30 {
						expiryIcon = "🟡"
					}
					fmt.Printf("    过期: %s %s (%s)\n", expiryIcon, expireTime.Format("2006-01-02 15:04:05"), expiryText)
				}

				if d.Issuer != "" {
					fmt.Printf("    颁发: %s\n", d.Issuer)
				}
				fmt.Println()
			}
		}
		return nil
	}

	// 获取要处理的域名
	domains := getDomainsToProcess(cfg, opts)
	if len(domains) == 0 {
		return fmt.Errorf("没有指定要处理的域名，请使用 -d 参数或在配置文件中设置 domains")
	}

	// 批量 reload 收集器（用于 --deploy 模式）
	pendingReloads := make(map[string]bool)
	deployedCount := 0

	// 循环处理每个域名
	for _, domain := range domains {
		slog.Info("开始处理域名", "domain", domain)
		var err error
		var reloadCmd string

		switch {
		case opts.Deploy:
			// 批量部署模式：部署证书但跳过 reload，最后统一执行
			reloadCmd, err = handleDeployBatch(ctx, wsClient, cfg, domain, opts)
			if reloadCmd != "" {
				pendingReloads[reloadCmd] = true
				deployedCount++
			}
		}

		if err != nil {
			slog.Error("处理域名失败", "domain", domain, "error", err)
		} else {
			slog.Info("成功处理域名", "domain", domain)
		}
		fmt.Println()
	}

	// 统一执行 reload 命令（去重后）
	if opts.Deploy && deployedCount > 0 && len(pendingReloads) > 0 {
		slog.Info("开始统一执行重载命令", "deployed", deployedCount, "commands", len(pendingReloads))
		executeReloadCommands(pendingReloads, opts.DryRun)
	}

	return nil
}

// getDomainsToProcess 获取要处理的域名列表
func getDomainsToProcess(cfg *config.ClientConfig, opts *CliOptions) []string {
	if opts.DomainsStr != "" {
		slog.Debug("从 '-d' 参数解析域名")
		parts := strings.Split(opts.DomainsStr, ",")
		var domains []string
		for _, d := range parts {
			if trimmed := strings.TrimSpace(d); trimmed != "" {
				domains = append(domains, trimmed)
			}
		}
		return domains
	}
	if len(cfg.Domains) > 0 {
		slog.Debug("从配置文件 'domains' 字段获取域名列表")
		return cfg.Domains
	}
	return nil
}

// handleDeployBatch 批量部署证书（不执行 reload）
// 返回需要执行的 reload 命令（如有），由调用方统一执行
func handleDeployBatch(ctx context.Context, wsClient *client.WSClient, cfg *config.ClientConfig, domain string, opts *CliOptions) (string, error) {
	slog.Debug("开始部署流程", "domain", domain, "dryRun", opts.DryRun)

	// 1. 创建工作空间
	ws := workspace.NewWorkspace(cfg.WorkDir, domain)
	if err := ws.Ensure(); err != nil {
		return "", fmt.Errorf("创建工作空间失败: %w", err)
	}

	// 2. 获取文件锁
	lock, err := ws.Lock()
	if err != nil {
		return "", fmt.Errorf("无法获取文件锁: %w", err)
	}
	defer lock.Unlock()

	// 3. 下载证书 (WebSocket request)
	certs, err := wsClient.DownloadCert(ctx, domain, opts.Force)
	if err != nil {
		return "", fmt.Errorf("下载证书失败: %w", err)
	}

	if certs.IsEmpty() {
		slog.Warn("未获取到证书数据")
		return "", nil
	}

	// 4. 保存到工作空间
	if err := ws.SaveCertificateFiles(certs); err != nil {
		return "", fmt.Errorf("保存证书失败: %w", err)
	}
	slog.Info("证书已保存到工作目录", "dir", ws.GetWorkDir())

	// 5. 查找部署配置
	site := findSiteConfig(cfg, domain)
	if site == nil {
		slog.Info("未找到此域名的站点部署配置，跳过部署步骤", "domain", domain)
		return "", nil
	}

	// 6. 确定 reload 命令
	// 优先级: 命令行 > 站点配置 > 全局默认
	reloadCmd := site.ReloadCmd
	if opts.ReloadCmd != "" {
		reloadCmd = opts.ReloadCmd
	} else if reloadCmd == "" && cfg.DefaultReloadCmd != "" {
		reloadCmd = cfg.DefaultReloadCmd
	}

	// 7. 准备部署配置（跳过 reload，由调用方统一执行）
	deployConfig := deployer.DeploymentConfig{
		Domain:        domain,
		CertPath:      site.CertPath,
		KeyPath:       site.KeyPath,
		FullchainPath: site.FullchainPath,
		ReloadCmd:     reloadCmd,
		SkipReload:    true, // 批量模式：跳过 reload
	}

	if opts.DryRun {
		slog.Info("[DryRun] 模式: 证书将会被部署",
			"cert", deployConfig.CertPath,
			"cmd", reloadCmd)
		return reloadCmd, nil
	}

	// 8. 执行部署（只写入文件，不执行 reload）
	d, err := deployer.NewDeployer(deployConfig)
	if err != nil {
		return "", fmt.Errorf("创建部署器失败: %w", err)
	}

	if err := d.Deploy(certs, opts.DryRun); err != nil {
		return "", fmt.Errorf("部署执行失败: %w", err)
	}

	return reloadCmd, nil
}

// executeReloadCommands 统一执行去重后的 reload 命令
func executeReloadCommands(commands map[string]bool, dryRun bool) {
	for cmd := range commands {
		if cmd == "" {
			continue
		}
		if dryRun {
			slog.Info("[DryRun] 将执行重载命令", "cmd", cmd)
			continue
		}
		slog.Info("执行重载命令", "cmd", cmd)
		output, err := command.Execute(context.Background(), cmd, 15*time.Second)
		if err != nil {
			slog.Error("重载命令执行失败", "cmd", cmd, "error", err, "output", output)
		} else {
			slog.Info("重载命令执行成功", "cmd", cmd, "output", output)
		}
	}
}

// findSiteConfig 查找域名对应的站点配置
func findSiteConfig(cfg *config.ClientConfig, domain string) *config.SiteDeployConfig {
	for i := range cfg.Sites {
		site := &cfg.Sites[i]
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

// runDaemon 运行 daemon 模式
func runDaemon(cfg *config.ClientConfig) {
	slog.Info("启动 Daemon 模式",
		"server", cfg.Server,
		"subscribe", cfg.Subscribe)

	// 设置默认值
	reconnectInterval := 30 * time.Second
	heartbeatInterval := 60 * time.Second
	reloadDebounce := 5 * time.Second
	syncInterval := 1 * time.Hour // 默认 1 小时同步一次

	if cfg.Daemon.ReconnectInterval > 0 {
		reconnectInterval = time.Duration(cfg.Daemon.ReconnectInterval) * time.Second
	}
	if cfg.Daemon.HeartbeatInterval > 0 {
		heartbeatInterval = time.Duration(cfg.Daemon.HeartbeatInterval) * time.Second
	}
	if cfg.Daemon.ReloadDebounce > 0 {
		reloadDebounce = time.Duration(cfg.Daemon.ReloadDebounce) * time.Second
	}
	// SyncInterval: 正数=自定义间隔，0/未设置=默认1小时，负数=禁用
	if cfg.Daemon.SyncInterval > 0 {
		syncInterval = time.Duration(cfg.Daemon.SyncInterval) * time.Second
	} else if cfg.Daemon.SyncInterval < 0 {
		// 负数表示禁用定时同步（重连同步仍然有效）
		syncInterval = 0
	}
	// SyncInterval == 0（未设置）时使用默认值 syncInterval = 1 * time.Hour

	// 获取客户端 ID（使用主机名）
	clientID, _ := os.Hostname()
	if clientID == "" {
		clientID = "acmedeliver-client"
	}

	// 直接使用配置中的站点配置（类型已统一为 config.SiteDeployConfig）
	daemonCfg := &client.DaemonConfig{
		ServerURL:         cfg.Server,
		Password:          cfg.Password,
		ClientID:          clientID,
		WorkDir:           cfg.WorkDir,
		Subscribe:         cfg.Subscribe,
		Sites:             cfg.Sites,
		ReconnectInterval: reconnectInterval,
		HeartbeatInterval: heartbeatInterval,
		ReloadDebounce:    reloadDebounce,
		SyncInterval:      syncInterval,
		TLSConfig: &client.TLSConfig{
			CaFile:             cfg.TLSCaFile,
			InsecureSkipVerify: cfg.TLSInsecureSkipVerify,
		},
	}

	daemon := client.NewDaemon(daemonCfg)

	// 启动配置热重载（如果指定了配置文件）
	if configFile != "" {
		watcher := config.NewClientConfigWatcher(configFile, cfg)

		// 注册配置更新回调
		watcher.RegisterCallback(func(oldCfg, newCfg *config.ClientConfig) {
			slog.Info("检测到配置变化，更新 Daemon 配置")
			daemon.UpdateConfig(newCfg.Subscribe, newCfg.Sites)
		})

		if err := watcher.Start(); err != nil {
			slog.Warn("启动配置热重载失败", "error", err)
		} else {
			defer watcher.Stop()
		}
	}

	if err := daemon.Run(context.Background()); err != nil {
		slog.Error("Daemon 运行失败", "error", err)
		os.Exit(1)
	}
}

// setupLogger 设置日志
// 修复：通过 HandlerOptions 正确设置日志级别
func setupLogger(debug bool) {
	var level slog.Level
	if debug {
		level = slog.LevelDebug
	} else {
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if debug {
		// 调试模式使用文本日志
		handler = slog.NewTextHandler(os.Stdout, opts)
	} else {
		// 生产模式使用 JSON 日志
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}
	slog.SetDefault(slog.New(handler))
}

// validateArgs 验证参数
func validateArgs(opts *CliOptions) error {
	// 检查 IP 模式冲突
	if opts.IPMode4 && opts.IPMode6 {
		return fmt.Errorf("-4 和 -6 选项不能同时使用")
	}

	// 检查操作参数冲突：--status 和 --deploy 互斥
	if opts.Status && opts.Deploy {
		return fmt.Errorf("不能同时指定 --status 和 --deploy")
	}

	return nil
}

// loadConfiguration 加载配置
// 优先级：命令行 > 环境变量 > 配置文件 > 默认值
func loadConfiguration(opts *CliOptions) *config.ClientConfig {
	// 如果未指定配置文件，检查当前目录是否存在 config.yaml
	if configFile == "" {
		if _, err := os.Stat("config.yaml"); err == nil {
			configFile = "config.yaml"
			slog.Info("检测到当前目录存在 config.yaml，自动加载")
		}
	}

	// 使用独立的客户端配置加载函数
	cfg, err := config.LoadClientConfig(configFile)
	if err != nil {
		slog.Warn("加载配置文件失败，使用默认配置", "error", err)
		cfg = &config.ClientConfig{
			Server:           "http://localhost:9090",
			Password:         "passwd",
			WorkDir:          "/tmp/acme",
			IPMode:           0,
			Debug:            opts.Debug,
			Domains:          []string{},
			DefaultReloadCmd: "",
		}
	} else if configFile != "" {
		slog.Info("从配置文件加载客户端配置", "file", configFile)
	}

	// 命令行参数覆盖（最高优先级）
	if opts.Server != "" {
		cfg.Server = opts.Server
	}
	if opts.Password != "" {
		cfg.Password = opts.Password
	}
	if opts.Debug {
		cfg.Debug = opts.Debug
	}
	if opts.IPMode4 {
		cfg.IPMode = 4
	} else if opts.IPMode6 {
		cfg.IPMode = 6
	}

	// 将命令行--reload-cmd 赋值给配置中的字段，以便后续统一处理
	if opts.ReloadCmd != "" {
		cfg.DefaultReloadCmd = opts.ReloadCmd
	}

	return cfg
}

// usage 显示帮助信息
func usage() {
	fmt.Fprintf(os.Stderr, `acmeDeliver 客户端 v%s

用法:
  acmedeliver-client [选项]

操作模式:
  --status              查询服务器运行状态（在线客户端 + 证书状态）
  --deploy              检查更新并部署证书
  --daemon              以守护进程模式运行

选项:
`, VERSION)
	flag.PrintDefaults()
	fmt.Fprintf(os.Stderr, `
部署说明:
  使用 --deploy 时，将根据配置文件中指定的路径部署证书：
  - cert_path:      证书路径
  - key_path:       私钥路径
  - fullchain_path: 证书链路径
  - reloadcmd:      部署后执行的命令

示例:
  # 查询服务器运行状态（在线客户端 + 证书状态）
  acmedeliver-client -s http://server:9090 -k passwd --status

  # 查询服务器运行状态
  acmedeliver-client -s http://server:9090 -k passwd --status

  # 检查更新并部署
  acmedeliver-client -c config.yaml -d example.com --deploy

  # 批量处理多个域名
  acmedeliver-client -c config.yaml -d "example.com,example.org" --deploy

  # 以守护进程模式运行
  acmedeliver-client -c config.yaml --daemon
`)
}

// formatDuration 格式化时间间隔
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d秒", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%d分钟", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		hours := int(d.Hours())
		minutes := int(d.Minutes()) % 60
		return fmt.Sprintf("%d小时%d分钟", hours, minutes)
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	return fmt.Sprintf("%d天%d小时", days, hours)
}
