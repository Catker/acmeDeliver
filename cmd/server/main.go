package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Catker/acmeDeliver/pkg/config"
	"github.com/Catker/acmeDeliver/pkg/server"
)

const VERSION = "3.0.3"

func main() {
	// 显示版本信息
	fmt.Printf("acmeDeliver v%s - 轻量证书分发服务 (推送模式)\n\n", VERSION)

	// 初始化配置
	if err := config.InitConfig(); err != nil {
		slog.Error("初始化配置失败", "error", err)
		os.Exit(1)
	}
	cfg := config.GetConfig()

	// 创建服务器实例（封装所有依赖，替代全局变量）
	srv, err := server.NewServer(cfg)
	if err != nil {
		slog.Error("创建服务器失败", "error", err)
		os.Exit(1)
	}

	// 创建可取消的上下文
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 设置信号处理（SIGINT/SIGTERM 用于优雅关闭）
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigChan
		slog.Info("🛑 收到信号，开始优雅关闭...", "signal", sig)
		cancel()
	}()

	// 运行服务器（阻塞直到上下文取消）
	if err := srv.Run(ctx); err != nil {
		slog.Error("服务器运行错误", "error", err)
		os.Exit(1)
	}
}

func init() {
	// 自定义帮助信息
	if len(os.Args) > 1 && (os.Args[1] == "-h" || os.Args[1] == "--help") {
		usage()
		os.Exit(0)
	}

	// 生成示例配置
	if len(os.Args) > 1 && os.Args[1] == "--gen-config" {
		fmt.Println(config.GenerateExampleConfig())
		os.Exit(0)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `acmeDeliver v%s - 轻量证书分发服务

使用方式:
  acmedeliver-server [选项]

选项:
`, VERSION)
	flag.PrintDefaults()
	fmt.Fprintf(os.Stderr, `
特殊命令:
  --gen-config  生成示例配置文件
  -h, --help    显示帮助信息

状态查询:
  请使用客户端查询服务器状态:
  acmedeliver-client -s http://server:9090 -k passwd --status

示例:
  # 使用配置文件
  acmedeliver-server -c config.yaml

  # 使用命令行参数
  acmedeliver-server -p 8080 -d /var/certs -k mypassword

  # 生成示例配置
  acmedeliver-server --gen-config > config.yaml
`)
}
