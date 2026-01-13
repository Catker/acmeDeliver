// Package command 提供安全的命令解析和执行功能
package command

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// Execute 安全执行命令
// 使用 Parse 解析命令，避免 shell 注入风险
// 包含超时保护，防止命令阻塞
//
// 参数:
//   - ctx: 上下文，用于取消控制
//   - cmd: 命令字符串
//   - timeout: 执行超时时间
//
// 返回:
//   - output: 命令输出（stdout + stderr）
//   - error: 执行错误
func Execute(ctx context.Context, cmd string, timeout time.Duration) (string, error) {
	cmdBin, args, err := Parse(cmd)
	if err != nil {
		return "", fmt.Errorf("命令解析失败: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	execCmd := exec.CommandContext(ctx, cmdBin, args...)
	output, err := execCmd.CombinedOutput()

	if ctx.Err() == context.DeadlineExceeded {
		return string(output), fmt.Errorf("命令执行超时 (%v)", timeout)
	}

	if err != nil {
		return string(output), fmt.Errorf("命令执行失败: %w", err)
	}

	return string(output), nil
}

// ExecuteWithStdio 执行命令并将输出直接写入 stdout/stderr
// 适用于需要实时显示输出的场景（如 daemon 模式）
//
// 参数:
//   - ctx: 上下文，用于取消控制
//   - cmd: 命令字符串
//   - timeout: 执行超时时间
//
// 返回:
//   - error: 执行错误
func ExecuteWithStdio(ctx context.Context, cmd string, timeout time.Duration) error {
	cmdBin, args, err := Parse(cmd)
	if err != nil {
		return fmt.Errorf("命令解析失败: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	execCmd := exec.CommandContext(ctx, cmdBin, args...)
	execCmd.Stdout = os.Stdout
	execCmd.Stderr = os.Stderr

	err = execCmd.Run()

	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("命令执行超时 (%v)", timeout)
	}

	return err
}
