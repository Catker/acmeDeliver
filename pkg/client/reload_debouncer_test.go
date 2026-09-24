package client

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// countingCmd 返回每次执行向计数文件追加一行、再执行 cond(计数文件) 的命令及计数函数
func countingCmd(t *testing.T, cond func(f string) string) (string, func() int) {
	t.Helper()
	f := filepath.Join(t.TempDir(), "count")
	cmd := "echo x >> " + f + "; " + cond(f)
	count := func() int {
		data, _ := os.ReadFile(f)
		return bytes.Count(data, []byte("\n"))
	}
	return cmd, count
}

func alwaysFail(string) string { return "false" }

func waitCount(t *testing.T, count func() int, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if count() >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("执行次数未达到 %d，实际 %d", want, count())
}

// 失败后退避重试，成功即停止
func TestReloadDebouncer_RetryUntilSuccess(t *testing.T) {
	// 第 3 次执行起成功
	cmd, count := countingCmd(t, func(f string) string {
		return `[ "$(wc -l < ` + f + `)" -ge 3 ]`
	})

	r := NewReloadDebouncer(10 * time.Millisecond)
	r.retryBackoff = 10 * time.Millisecond
	r.Trigger(cmd)

	waitCount(t, count, 3)
	time.Sleep(300 * time.Millisecond)
	if n := count(); n != 3 {
		t.Fatalf("成功后不应继续重试，执行次数 %d", n)
	}
}

// 重试次数耗尽后停止
func TestReloadDebouncer_RetryExhausted(t *testing.T) {
	cmd, count := countingCmd(t, alwaysFail)
	r := NewReloadDebouncer(10 * time.Millisecond)
	r.retryBackoff = 5 * time.Millisecond
	r.maxRetries = 2
	r.Trigger(cmd)

	waitCount(t, count, 3) // 首次 + 2 次重试
	time.Sleep(300 * time.Millisecond)
	if n := count(); n != 3 {
		t.Fatalf("重试耗尽后不应继续执行，执行次数 %d", n)
	}
}

// 等待重试期间再次 Trigger：取消原重试并重新计数，不重复执行
func TestReloadDebouncer_TriggerDuringRetryMerged(t *testing.T) {
	cmd, count := countingCmd(t, alwaysFail)
	r := NewReloadDebouncer(10 * time.Millisecond)
	r.retryBackoff = 400 * time.Millisecond
	r.maxRetries = 1
	r.Trigger(cmd)

	waitCount(t, count, 1)
	r.Trigger(cmd) // 原定 400ms 后的重试应被取消
	waitCount(t, count, 2)
	time.Sleep(200 * time.Millisecond)
	if n := count(); n != 2 {
		t.Fatalf("重试等待期间不应额外执行，执行次数 %d", n)
	}

	// 新 Trigger 的执行失败后只安排一次重试（maxRetries=1）
	waitCount(t, count, 3)
	time.Sleep(600 * time.Millisecond)
	if n := count(); n != 3 {
		t.Fatalf("原重试未被合并，执行次数 %d", n)
	}
}

// Flush 立即执行等待重试中的命令，且不再安排重试
func TestReloadDebouncer_FlushExecutesRetryWithoutRescheduling(t *testing.T) {
	cmd, count := countingCmd(t, alwaysFail)
	r := NewReloadDebouncer(10 * time.Millisecond)
	r.retryBackoff = time.Hour
	r.Trigger(cmd)

	// 等待首次失败后已安排重试
	deadline := time.Now().Add(3 * time.Second)
	for {
		r.mu.Lock()
		n := len(r.retryTimers)
		r.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("首次失败后未安排重试")
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.Flush()
	if n := count(); n != 2 {
		t.Fatalf("Flush 应立即执行等待重试的命令，执行次数 %d", n)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.retryTimers) != 0 {
		t.Fatalf("Flush 后不应再安排重试: %d", len(r.retryTimers))
	}
}

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
