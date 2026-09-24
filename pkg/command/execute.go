// Package command 解析并执行 reload 命令（不经过 shell）
package command

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// Execute 解析并执行命令，输出直接写入 stdout/stderr；超时后终止进程
func Execute(cmd string, timeout time.Duration) error {
	bin, args, err := Parse(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	execCmd := exec.CommandContext(ctx, bin, args...)
	execCmd.Stdout = os.Stdout
	execCmd.Stderr = os.Stderr
	err = execCmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("命令执行超时 (%v)", timeout)
	}
	return err
}
