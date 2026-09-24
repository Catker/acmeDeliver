// Package command 执行 reload 命令（通过 sh -c，命令仅来自本地配置）
package command

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Execute 通过 sh -c 执行命令（与 acme.sh --reloadcmd 语义一致，支持 &&、| 等 shell 语法），
// 输出直接写入 stdout/stderr；超时后终止进程
func Execute(cmd string, timeout time.Duration) error {
	if strings.TrimSpace(cmd) == "" {
		return fmt.Errorf("空命令")
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	execCmd := exec.CommandContext(ctx, "sh", "-c", cmd)
	execCmd.Stdout = os.Stdout
	execCmd.Stderr = os.Stderr
	err := execCmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("命令执行超时 (%v)", timeout)
	}
	return err
}
