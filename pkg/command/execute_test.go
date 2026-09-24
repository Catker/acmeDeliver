package command

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestExecute(t *testing.T) {
	if err := Execute("true", time.Second); err != nil {
		t.Errorf("true 应成功: %v", err)
	}
	if err := Execute("false", time.Second); err == nil {
		t.Error("false 应返回错误")
	}
	if err := Execute("   ", time.Second); err == nil {
		t.Error("空命令应返回错误")
	}
}

// shell 语法（&&、重定向）应由 sh 解释
func TestExecute_ShellSyntax(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	if err := Execute("echo a > "+out+" && echo b >> "+out, time.Second); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "a\nb\n" {
		t.Errorf("输出 = %q, want %q", data, "a\nb\n")
	}
}

func TestExecute_Timeout(t *testing.T) {
	start := time.Now()
	if err := Execute("sleep 2", 100*time.Millisecond); err == nil {
		t.Fatal("超时应返回错误")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("超时未生效，耗时 %v", elapsed)
	}
}
