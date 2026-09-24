package client

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 执行期间 Trigger 的命令不得丢失，应在当前执行结束后被执行（B4）
func TestReloadDebouncer_TriggerDuringExecutionNotLost(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "reloaded")
	r := NewReloadDebouncer(10 * time.Millisecond)

	r.Trigger("sleep 0.3")
	time.Sleep(100 * time.Millisecond) // 此时第一次执行正在进行
	r.Trigger("touch " + marker)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("执行期间触发的 reload 命令未被执行")
}

// Flush 应立即同步执行防抖中的命令（daemon 退出前不丢失 reload）
func TestReloadDebouncer_FlushExecutesPending(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "flushed")
	r := NewReloadDebouncer(time.Hour)

	r.Trigger("touch " + marker)
	r.Flush()

	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("Flush 后命令应已执行: %v", err)
	}
}
