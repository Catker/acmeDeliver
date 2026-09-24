package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Catker/acmeDeliver/pkg/config"
	ws "github.com/Catker/acmeDeliver/pkg/websocket"
)

// shortenRetryDelay 将重试退避缩短到 1ms，避免测试长时间睡眠
func shortenRetryDelay(t *testing.T) {
	t.Helper()
	old := deployRetryBaseDelay
	deployRetryBaseDelay = time.Millisecond
	t.Cleanup(func() { deployRetryBaseDelay = old })
}

// writeTestCertFiles 在目录中写入三份证书源文件
func writeTestCertFiles(t *testing.T, dir, cert, key, fullchain string) {
	t.Helper()
	for name, content := range map[string]string{
		"cert.pem":      cert,
		"key.pem":       key,
		"fullchain.pem": fullchain,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func assertFileContentPerm(t *testing.T, path, wantContent string, wantPerm os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if info.Mode().Perm() != wantPerm {
		t.Errorf("文件 %s 权限 = %o, want %o", path, info.Mode().Perm(), wantPerm)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != wantContent {
		t.Errorf("文件 %s 内容 = %q, want %q", path, string(data), wantContent)
	}
}

func newTestDaemon(t *testing.T, workDir string, sites []config.SiteDeployConfig) *Daemon {
	t.Helper()
	return NewDaemon(&DaemonConfig{
		ServerURL:         "ws://test.invalid",
		Password:          "test",
		ClientID:          "test-client",
		WorkDir:           workDir,
		Subscribe:         []string{"example.com"},
		ReconnectInterval: time.Second,
		HeartbeatInterval: time.Hour,
		Sites:             sites,
	})
}

// ============================================
// deployCertFiles：错误传播与写入行为
// ============================================

func TestDeployCertFiles_WritesContentAndPerms(t *testing.T) {
	srcDir := t.TempDir()
	writeTestCertFiles(t, srcDir, "cert-data", "key-data", "chain-data")

	dstDir := t.TempDir()
	site := &config.SiteDeployConfig{
		Domain:        "example.com",
		CertPath:      filepath.Join(dstDir, "{domain}", "cert.pem"),
		KeyPath:       filepath.Join(dstDir, "{domain}", "key.pem"),
		FullchainPath: filepath.Join(dstDir, "{domain}", "fullchain.pem"),
	}

	d := newTestDaemon(t, t.TempDir(), nil)
	if err := d.deployCertFiles("example.com", srcDir, site); err != nil {
		t.Fatalf("deployCertFiles() error = %v", err)
	}

	// {domain} 占位符替换 + 内容 + 权限（key.pem 0600，其余 0644）
	assertFileContentPerm(t, filepath.Join(dstDir, "example.com", "cert.pem"), "cert-data", 0644)
	assertFileContentPerm(t, filepath.Join(dstDir, "example.com", "key.pem"), "key-data", 0600)
	assertFileContentPerm(t, filepath.Join(dstDir, "example.com", "fullchain.pem"), "chain-data", 0644)

	// 原子写入不应残留临时文件
	if matches, _ := filepath.Glob(filepath.Join(dstDir, "example.com", "*.tmp-*")); len(matches) != 0 {
		t.Errorf("不应残留临时文件: %v", matches)
	}
}

func TestDeployCertFiles_PropagatesErrors(t *testing.T) {
	srcDir := t.TempDir()
	// 只写 cert.pem，缺 key.pem
	if err := os.WriteFile(filepath.Join(srcDir, "cert.pem"), []byte("cert"), 0644); err != nil {
		t.Fatal(err)
	}

	dstDir := t.TempDir()
	site := &config.SiteDeployConfig{
		KeyPath: filepath.Join(dstDir, "key.pem"),
	}

	d := newTestDaemon(t, t.TempDir(), nil)
	err := d.deployCertFiles("example.com", srcDir, site)
	if err == nil {
		t.Fatal("源 key.pem 缺失时 deployCertFiles() 应返回错误，得到 nil")
	}
	if !strings.Contains(err.Error(), "key.pem") {
		t.Errorf("错误应指明失败的文件，得到: %v", err)
	}
}

func TestDeployCertFiles_WriteFailurePropagates(t *testing.T) {
	srcDir := t.TempDir()
	writeTestCertFiles(t, srcDir, "cert", "key", "chain")

	// 目标路径是一个已存在的目录：临时文件重命名替换目录必然失败
	dstRoot := t.TempDir()
	blocker := filepath.Join(dstRoot, "blocker")
	if err := os.MkdirAll(blocker, 0755); err != nil {
		t.Fatal(err)
	}

	site := &config.SiteDeployConfig{CertPath: blocker}
	d := newTestDaemon(t, t.TempDir(), nil)

	err := d.deployCertFiles("example.com", srcDir, site)
	if err == nil {
		t.Fatal("目标不可写时 deployCertFiles() 应返回错误，得到 nil")
	}
	// 失败后不得残留本次写入的临时文件
	if matches, _ := filepath.Glob(filepath.Join(dstRoot, "blocker.tmp-*")); len(matches) != 0 {
		t.Errorf("写入失败后应清理临时文件，残留: %v", matches)
	}
}

// 回归：空源文件（如空 key.pem）必须拒绝部署，不得清空已有目标；
// 与 CLI deployer 的空内容规则共用
func TestDeployCertFiles_EmptySourceContentRejected(t *testing.T) {
	for _, emptyName := range []string{"cert.pem", "key.pem", "fullchain.pem"} {
		t.Run("空源文件_"+emptyName, func(t *testing.T) {
			shortenRetryDelay(t)

			srcDir := t.TempDir()
			writeTestCertFiles(t, srcDir, "cert-data", "key-data", "chain-data")
			// 把待测源文件清空
			if err := os.WriteFile(filepath.Join(srcDir, emptyName), []byte{}, 0644); err != nil {
				t.Fatal(err)
			}

			// 目标预先存在有效内容，空源不得清空它们
			dstDir := t.TempDir()
			sentinel := map[string]string{
				"cert.pem":      "existing-cert",
				"key.pem":       "existing-key",
				"fullchain.pem": "existing-chain",
			}
			for name, content := range sentinel {
				if err := os.WriteFile(filepath.Join(dstDir, name), []byte(content), 0644); err != nil {
					t.Fatal(err)
				}
			}

			site := &config.SiteDeployConfig{
				Domain:        "example.com",
				CertPath:      filepath.Join(dstDir, "cert.pem"),
				KeyPath:       filepath.Join(dstDir, "key.pem"),
				FullchainPath: filepath.Join(dstDir, "fullchain.pem"),
			}
			d := newTestDaemon(t, t.TempDir(), nil)

			err := d.deployCertFilesWithRetry("example.com", srcDir, site, deployMaxRetries)
			if err == nil {
				t.Fatalf("源文件 %s 为空时应返回错误，得到 nil", emptyName)
			}
			if !strings.Contains(err.Error(), emptyName) {
				t.Errorf("错误应指明空文件 %s，得到: %v", emptyName, err)
			}

			// 已有目标内容原样保留
			for name, want := range sentinel {
				data, readErr := os.ReadFile(filepath.Join(dstDir, name))
				if readErr != nil {
					t.Fatalf("read %s: %v", name, readErr)
				}
				if string(data) != want {
					t.Errorf("空源部署被拒后，目标 %s 应保持原内容，得到 %q", name, string(data))
				}
			}
		})
	}
}

// ============================================
// 重试语义：瞬时失败可恢复，持续失败到上限
// ============================================

func TestWithRetry_FirstFailureThenSuccess(t *testing.T) {
	shortenRetryDelay(t)

	attempts := 0
	op := func() error {
		attempts++
		if attempts == 1 {
			return errTestTransient
		}
		return nil
	}

	if err := withRetry(3, op); err != nil {
		t.Fatalf("首次失败后成功应返回 nil，得到 %v", err)
	}
	if attempts != 2 {
		t.Errorf("应在第 2 次尝试成功，实际尝试 %d 次", attempts)
	}
}

var errTestTransient = &testError{"瞬时错误"}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }

func TestWithRetry_PersistentFailureReachesLimit(t *testing.T) {
	shortenRetryDelay(t)

	attempts := 0
	op := func() error {
		attempts++
		return errTestTransient
	}

	err := withRetry(3, op)
	if err == nil {
		t.Fatal("持续失败应返回最后一次错误")
	}
	if attempts != 3 {
		t.Errorf("应尝试满 3 次，实际尝试 %d 次", attempts)
	}

	// 自定义上限也应被尊重
	attempts = 0
	if err := withRetry(2, op); err == nil {
		t.Fatal("持续失败应返回错误")
	}
	if attempts != 2 {
		t.Errorf("maxRetries=2 时应尝试 2 次，实际 %d 次", attempts)
	}
}

func TestDeployCertFilesWithRetry_PersistentFailureReturnsError(t *testing.T) {
	shortenRetryDelay(t)

	srcDir := t.TempDir()
	writeTestCertFiles(t, srcDir, "cert", "key", "chain")

	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.MkdirAll(blocker, 0755); err != nil {
		t.Fatal(err)
	}

	site := &config.SiteDeployConfig{CertPath: blocker}
	d := newTestDaemon(t, t.TempDir(), nil)

	if err := d.deployCertFilesWithRetry("example.com", srcDir, site, 3); err == nil {
		t.Fatal("持续失败时 deployCertFilesWithRetry() 应最终返回错误")
	}
}

// ============================================
// receiveCert：保存 + 部署 + reload 命令返回
// ============================================

func TestReceiveCert_FullSuccess(t *testing.T) {
	shortenRetryDelay(t)
	workDir := t.TempDir()
	siteDir := t.TempDir()

	sites := []config.SiteDeployConfig{{
		Domain:        "example.com",
		CertPath:      filepath.Join(siteDir, "{domain}", "cert.pem"),
		KeyPath:       filepath.Join(siteDir, "{domain}", "key.pem"),
		FullchainPath: filepath.Join(siteDir, "{domain}", "fullchain.pem"),
		ReloadCmd:     "echo reload-example",
	}}
	d := newTestDaemon(t, workDir, sites)

	push := &ws.CertPushData{
		Domain: "example.com",
		Files: map[string][]byte{
			"cert.pem":      []byte("cert-data"),
			"key.pem":       []byte("key-data"),
			"fullchain.pem": []byte("chain-data"),
			"time.log":      []byte("1757011200\n"),
		},
	}

	reloadCmd, err := d.receiveCert(push)
	if err != nil {
		t.Fatalf("receiveCert() error = %v", err)
	}
	if reloadCmd != "echo reload-example" {
		t.Errorf("reloadCmd = %q, want %q", reloadCmd, "echo reload-example")
	}

	// 工作目录：内容 + 权限
	assertFileContentPerm(t, filepath.Join(workDir, "example.com", "cert.pem"), "cert-data", 0644)
	assertFileContentPerm(t, filepath.Join(workDir, "example.com", "key.pem"), "key-data", 0600)
	assertFileContentPerm(t, filepath.Join(workDir, "example.com", "time.log"), "1757011200\n", 0644)

	// 站点目标：{domain} 替换 + 权限
	assertFileContentPerm(t, filepath.Join(siteDir, "example.com", "cert.pem"), "cert-data", 0644)
	assertFileContentPerm(t, filepath.Join(siteDir, "example.com", "key.pem"), "key-data", 0600)
	assertFileContentPerm(t, filepath.Join(siteDir, "example.com", "fullchain.pem"), "chain-data", 0644)
}

func TestReceiveCert_NoSiteConfigSkipsDeploy(t *testing.T) {
	workDir := t.TempDir()
	d := newTestDaemon(t, workDir, nil)

	reloadCmd, err := d.receiveCert(&ws.CertPushData{
		Domain: "example.com",
		Files:  map[string][]byte{"cert.pem": []byte("cert-data")},
	})
	if err != nil {
		t.Fatalf("无站点配置时 receiveCert() 不应报错: %v", err)
	}
	if reloadCmd != "" {
		t.Errorf("无站点配置时 reloadCmd 应为空，得到 %q", reloadCmd)
	}
}

func TestReceiveCert_SaveFailurePropagates(t *testing.T) {
	d := newTestDaemon(t, t.TempDir(), nil)

	_, err := d.receiveCert(&ws.CertPushData{
		Domain: "example.com",
		Files:  map[string][]byte{"../evil.pem": []byte("bad")},
	})
	if err == nil {
		t.Fatal("非法文件名时 receiveCert() 应返回错误")
	}
}

func TestReceiveCert_DeployFailurePropagates(t *testing.T) {
	shortenRetryDelay(t)

	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.MkdirAll(blocker, 0755); err != nil {
		t.Fatal(err)
	}
	sites := []config.SiteDeployConfig{{
		Domain:   "example.com",
		CertPath: blocker,
	}}
	d := newTestDaemon(t, t.TempDir(), sites)

	_, err := d.receiveCert(&ws.CertPushData{
		Domain: "example.com",
		Files:  map[string][]byte{"cert.pem": []byte("cert-data")},
	})
	if err == nil {
		t.Fatal("部署失败时 receiveCert() 应返回错误")
	}
	if !strings.Contains(err.Error(), "部署证书失败") {
		t.Errorf("错误应包含部署失败上下文，得到: %v", err)
	}
}

// ============================================
// handleCertPush：失败不 ACK 成功、不触发 reload
// ============================================

// setupDaemonConnPair 建立一对真实 WebSocket 连接，Daemon 侧写入、测试侧读取
func setupDaemonConnPair(t *testing.T, cfg *DaemonConfig) (*Daemon, *websocket.Conn) {
	t.Helper()

	serverConns := make(chan *websocket.Conn, 1)
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		serverConns <- c
	}))
	t.Cleanup(srv.Close)

	url := "ws://" + srv.Listener.Addr().String()
	clientConn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("连接测试 WebSocket 服务器失败: %v", err)
	}
	t.Cleanup(func() { clientConn.Close() })

	if cfg.ReloadDebounce <= 0 {
		cfg.ReloadDebounce = time.Hour // 测试期内不真正执行 reload 命令
	}
	d := NewDaemon(cfg)
	d.conn = clientConn

	serverConn := <-serverConns
	t.Cleanup(func() { serverConn.Close() })
	return d, serverConn
}

func readCertAck(t *testing.T, conn *websocket.Conn) ws.CertAck {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("设置读超时失败: %v", err)
	}
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("读取 CertAck 失败: %v", err)
	}
	var msg ws.Message
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("解析消息失败: %v", err)
	}
	if msg.Type != ws.MsgTypeCertAck {
		t.Fatalf("消息类型 = %q, want %q", msg.Type, ws.MsgTypeCertAck)
	}
	var ack ws.CertAck
	if err := msg.ParseData(&ack); err != nil {
		t.Fatalf("解析 CertAck 失败: %v", err)
	}
	return ack
}

// pendingReloads 返回防抖器中已排队、尚未执行的 reload 命令数
func pendingReloads(r *ReloadDebouncer) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.timer != nil {
		r.timer.Stop()
	}
	return len(r.pendingCmds)
}

func TestHandleCertPush_DeployFailureSendsFailureAckWithoutReload(t *testing.T) {
	shortenRetryDelay(t)

	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.MkdirAll(blocker, 0755); err != nil {
		t.Fatal(err)
	}
	cfg := &DaemonConfig{
		ServerURL:         "ws://test.invalid",
		Password:          "test",
		ClientID:          "test-client",
		WorkDir:           t.TempDir(),
		ReconnectInterval: time.Second,
		HeartbeatInterval: time.Hour,
		Sites: []config.SiteDeployConfig{{
			Domain:    "example.com",
			CertPath:  blocker,
			ReloadCmd: "echo should-not-run",
		}},
	}

	d, serverConn := setupDaemonConnPair(t, cfg)

	d.handleCertPush(&ws.CertPushData{
		Domain: "example.com",
		Files:  map[string][]byte{"cert.pem": []byte("cert-data")},
	})

	ack := readCertAck(t, serverConn)
	if ack.Domain != "example.com" {
		t.Errorf("ACK Domain = %q, want example.com", ack.Domain)
	}
	if ack.Success {
		t.Error("部署失败时不应发送成功 ACK")
	}
	if ack.Message == "" {
		t.Error("失败 ACK 应携带错误信息")
	}
	if n := pendingReloads(d.reloadDebouncer); n != 0 {
		t.Errorf("部署失败时不应触发 reload，防抖队列中有 %d 条命令", n)
	}
}

func TestHandleCertPush_SuccessSendsAckAndQueuesReload(t *testing.T) {
	shortenRetryDelay(t)
	siteDir := t.TempDir()
	cfg := &DaemonConfig{
		ServerURL:         "ws://test.invalid",
		Password:          "test",
		ClientID:          "test-client",
		WorkDir:           t.TempDir(),
		ReconnectInterval: time.Second,
		HeartbeatInterval: time.Hour,
		Sites: []config.SiteDeployConfig{{
			Domain:        "example.com",
			CertPath:      filepath.Join(siteDir, "{domain}", "cert.pem"),
			KeyPath:       filepath.Join(siteDir, "{domain}", "key.pem"),
			FullchainPath: filepath.Join(siteDir, "{domain}", "fullchain.pem"),
			ReloadCmd:     "echo reload-ok",
		}},
	}

	d, serverConn := setupDaemonConnPair(t, cfg)

	d.handleCertPush(&ws.CertPushData{
		Domain: "example.com",
		Files: map[string][]byte{
			"cert.pem":      []byte("cert-data"),
			"key.pem":       []byte("key-data"),
			"fullchain.pem": []byte("chain-data"),
		},
	})

	ack := readCertAck(t, serverConn)
	if !ack.Success {
		t.Errorf("部署成功应发送成功 ACK，得到 message=%q", ack.Message)
	}
	if n := pendingReloads(d.reloadDebouncer); n != 1 {
		t.Errorf("部署成功应将 reload 命令加入防抖队列，队列长度 = %d, want 1", n)
	}
}

// ============================================
// connectAndServe：连接结束后后台 goroutine 全部退出，认证失败断开连接
// ============================================

// 服务端拒绝认证并断开；每次 connectAndServe 返回认证错误，且不泄漏后台 goroutine（B1/B5）
func TestConnectAndServe_AuthFailureNoGoroutineLeak(t *testing.T) {
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		if _, _, err := c.ReadMessage(); err != nil { // 读取 auth 请求
			return
		}
		msg, _ := ws.NewMessage(ws.MsgTypeAuthResult, &ws.AuthResponse{Success: false, Message: "bad"})
		data, _ := json.Marshal(msg)
		c.WriteMessage(websocket.TextMessage, data)
		c.ReadMessage() // 等待客户端主动断开
	}))
	defer srv.Close()

	d := NewDaemon(&DaemonConfig{
		ServerURL:         "ws://" + srv.Listener.Addr().String(),
		Password:          "test",
		ClientID:          "test-client",
		WorkDir:           t.TempDir(),
		HeartbeatInterval: time.Hour,
		SyncInterval:      time.Hour,
	})

	baseline := runtime.NumGoroutine()
	for i := 0; i < 5; i++ {
		err := d.connectAndServe(context.Background())
		if err == nil || !strings.Contains(err.Error(), "认证失败") {
			t.Fatalf("认证失败时应返回认证错误，got %v", err)
		}
	}

	// 每次连接会启动 3 个后台 goroutine；泄漏时 5 次连接将多出约 15 个
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > baseline+2 {
		if time.Now().After(deadline) {
			t.Fatalf("连接结束后 goroutine 未回收：baseline=%d, now=%d", baseline, runtime.NumGoroutine())
		}
		time.Sleep(20 * time.Millisecond)
	}
}
