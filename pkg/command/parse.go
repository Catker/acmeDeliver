package command

import (
	"fmt"

	"github.com/google/shlex"
)

// Parse 将命令字符串按 Shell 风格（引号、转义）拆分为命令和参数。
// 命令直接 exec 执行、不经过 shell，;、|、$() 等只是普通参数字符，不会被解释。
func Parse(cmd string) (string, []string, error) {
	args, err := shlex.Split(cmd)
	if err != nil {
		return "", nil, fmt.Errorf("命令解析失败: %w", err)
	}
	if len(args) == 0 {
		return "", nil, fmt.Errorf("空命令")
	}
	return args[0], args[1:], nil
}
