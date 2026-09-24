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

const VERSION = "3.1.1"

func main() {
	// 显示版本信息
	fmt.Printf("acmeDeliver v%s - 轻量证书分发服务\n\n", VERSION)

	// -h/--help 由 InitConfig 内的 flag.Parse 触发，此时参数已定义
	flag.Usage = usage

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

	// 将系统信号统一转换为上下文取消，交给 Run(ctx) 处理关闭
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 运行服务器（阻塞直到上下文取消）
	if err := srv.Run(ctx); err != nil {
		slog.Error("服务器运行错误", "error", err)
		os.Exit(1)
	}
}

func init() {
	// 生成示例配置
	if len(os.Args) > 1 && os.Args[1] == "--gen-config" {
		fmt.Println(config.GenerateExampleConfig())
		os.Exit(0)
	}
}

func usage() {
	// 版本横幅已由 main 打印
	fmt.Fprint(os.Stderr, `使用方式:
  acmedeliver-server [选项]

选项:
`)
	// 不用 PrintDefaults：flag 默认值来自配置文件/环境变量，会把密钥打印出来
	flag.VisitAll(func(f *flag.Flag) {
		fmt.Fprintf(os.Stderr, "  -%-10s %s\n", f.Name, f.Usage)
	})
	fmt.Fprintf(os.Stderr, `
特殊命令:
  --gen-config  生成示例配置文件
  -h, --help    显示帮助信息

状态查询:
  请使用客户端查询服务器状态:
  acmedeliver-client -s http://server:9090 -k your-password --status

示例:
  # 使用配置文件
  acmedeliver-server -c config.yaml

  # 使用命令行参数
  acmedeliver-server -p 8080 -d /var/certs -k mypassword

  # 生成示例配置
  acmedeliver-server --gen-config > config.yaml
`)
}
