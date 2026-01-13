// Package command 提供安全的命令解析功能
package command

import (
	"fmt"
	"strings"

	"github.com/google/shlex"
)

// validateCommand 验证命令是否安全
func validateCommand(cmd string) error {
	// 检查危险字符和模式
	dangerousPatterns := []string{
		";",  // 命令分隔符
		"&",  // 后台执行
		"|",  // 管道
		"`",  // 命令替换
		"$(", // 命令替换
		"${", // 变量替换
		">>", // 追加重定向
		">",  // 重定向
		"<",  // 输入重定向
		"&&", // 逻辑与
		"||", // 逻辑或
	}

	for _, pattern := range dangerousPatterns {
		if strings.Contains(cmd, pattern) {
			return fmt.Errorf("命令包含不安全的字符: %s", pattern)
		}
	}

	// 检查是否以 sudo 开头（需要特殊处理）
	if strings.HasPrefix(strings.TrimSpace(cmd), "sudo ") {
		return fmt.Errorf("不建议在 reloadcmd 中使用 sudo")
	}

	return nil
}

// Parse 将命令字符串解析为命令和参数，支持 Shell 风格的引号处理。
// 使用 github.com/google/shlex 进行词法分析，正确处理单引号、双引号和转义字符。
// 该函数首先验证命令安全性，拒绝包含 shell 特殊字符的命令。
func Parse(cmd string) (string, []string, error) {
	if err := validateCommand(cmd); err != nil {
		return "", nil, err
	}

	// 使用 shlex 进行 Shell 风格解析
	args, err := shlex.Split(cmd)
	if err != nil {
		return "", nil, fmt.Errorf("命令解析失败: %w", err)
	}

	if len(args) == 0 {
		return "", nil, fmt.Errorf("空命令")
	}

	// 第一个参数是命令，其余是参数
	return args[0], args[1:], nil
}
