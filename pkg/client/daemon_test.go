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
		Sites:             sites,
	})
}

// ============================================
// receiveCert：保存 + 部署 + reload 命令返回
// ============================================

func TestReceiveCert_FullSuccess(t *testing.T) {
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

func TestReceiveCert_InvalidDomainRejected(t *testing.T) {
	d := newTestDaemon(t, t.TempDir(), nil)

	_, err := d.receiveCert(&ws.CertPushData{
		Domain: "../evil",
		Files:  map[string][]byte{"cert.pem": []byte("bad")},
	})
	if err == nil {
		t.Fatal("非法域名时 receiveCert() 应返回错误")
	}
}

func TestReceiveCert_DefaultReloadCmdFallback(t *testing.T) {
	sites := []config.SiteDeployConfig{{Domain: "example.com"}}
	d := newTestDaemon(t, t.TempDir(), sites)
	d.config.DefaultReloadCmd = "echo default"

	reloadCmd, err := d.receiveCert(&ws.CertPushData{
		Domain: "example.com",
		Files:  map[string][]byte{"cert.pem": []byte("cert-data")},
	})
	if err != nil {
		t.Fatalf("receiveCert() error = %v", err)
	}
	if reloadCmd != "echo default" {
		t.Errorf("站点未配置 reloadcmd 时应使用 default_reload_cmd，得到 %q", reloadCmd)
	}
}

func TestReceiveCert_DeployFailurePropagates(t *testing.T) {

	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.MkdirAll(blocker, 0755); err != nil {
		t.Fatal(err)
	}
	sites := []config.SiteDeployConfig{{
		Domain:   "example.com",
		CertPath: blocker,
	}}
	workDir := t.TempDir()
	d := newTestDaemon(t, workDir, sites)

	_, err := d.receiveCert(&ws.CertPushData{
		Domain: "example.com",
		Files: map[string][]byte{
			"cert.pem": []byte("cert-data"),
			"time.log": []byte("1757011200\n"),
		},
	})
	if err == nil {
		t.Fatal("部署失败时 receiveCert() 应返回错误")
	}
	if !strings.Contains(err.Error(), "部署证书失败") {
		t.Errorf("错误应包含部署失败上下文，得到: %v", err)
	}
	// time.log 必须最后写：部署失败时不写入，下次同步服务端仍会推送
	if _, err := os.Stat(filepath.Join(workDir, "example.com", "time.log")); !os.IsNotExist(err) {
		t.Errorf("部署失败时工作目录不应写入 time.log，stat err = %v", err)
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
	siteDir := t.TempDir()
	cfg := &DaemonConfig{
		ServerURL:         "ws://test.invalid",
		Password:          "test",
		ClientID:          "test-client",
		WorkDir:           t.TempDir(),
		ReconnectInterval: time.Second,
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

// 推送的 time.log 与本地相比：本地不旧于推送时跳过部署与 reload，仍发送成功 ACK；本地更旧时正常部署
func TestHandleCertPush_SkipsWhenLocalUpToDate(t *testing.T) {
	tests := []struct {
		name       string
		localTS    string
		pushTS     string
		wantDeploy bool
	}{
		{"本地与推送相同则跳过", "1757011200", "1757011200", false},
		{"本地较新则跳过", "1757011300", "1757011200", false},
		{"本地较旧则部署", "1757011100", "1757011200", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workDir := t.TempDir()
			siteDir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(workDir, "example.com"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(workDir, "example.com", "time.log"), []byte(tt.localTS+"\n"), 0644); err != nil {
				t.Fatal(err)
			}
			certPath := filepath.Join(siteDir, "cert.pem")
			cfg := &DaemonConfig{
				ServerURL:         "ws://test.invalid",
				Password:          "test",
				ClientID:          "test-client",
				WorkDir:           workDir,
				ReconnectInterval: time.Second,
				Sites: []config.SiteDeployConfig{{
					Domain:    "example.com",
					CertPath:  certPath,
					ReloadCmd: "echo reload-ok",
				}},
			}
			d, serverConn := setupDaemonConnPair(t, cfg)

			d.handleCertPush(&ws.CertPushData{
				Domain: "example.com",
				Files: map[string][]byte{
					"cert.pem": []byte("cert-data"),
					"time.log": []byte(tt.pushTS + "\n"),
				},
			})

			if ack := readCertAck(t, serverConn); !ack.Success {
				t.Errorf("应发送成功 ACK，得到 message=%q", ack.Message)
			}
			_, statErr := os.Stat(certPath)
			if deployed := statErr == nil; deployed != tt.wantDeploy {
				t.Errorf("目标文件已写入 = %v, want %v", deployed, tt.wantDeploy)
			}
			wantReloads := 0
			if tt.wantDeploy {
				wantReloads = 1
			}
			if n := pendingReloads(d.reloadDebouncer); n != wantReloads {
				t.Errorf("防抖队列长度 = %d, want %d", n, wantReloads)
			}
			if got := ReadLocalTimestamp(workDir, "example.com"); tt.wantDeploy && got != 1757011200 {
				t.Errorf("部署后本地 time.log = %d, want 1757011200", got)
			}
		})
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
		ServerURL:    "ws://" + srv.Listener.Addr().String(),
		Password:     "test",
		ClientID:     "test-client",
		WorkDir:      t.TempDir(),
		SyncInterval: time.Hour,
	})

	baseline := runtime.NumGoroutine()
	for i := 0; i < 5; i++ {
		err := d.connectAndServe(context.Background())
		if err == nil || !strings.Contains(err.Error(), "认证失败") {
			t.Fatalf("认证失败时应返回认证错误，got %v", err)
		}
	}

	// 每次连接会启动后台 goroutine（定时同步、读取）；泄漏时 5 次连接将明显多出
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > baseline+2 {
		if time.Now().After(deadline) {
			t.Fatalf("连接结束后 goroutine 未回收：baseline=%d, now=%d", baseline, runtime.NumGoroutine())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// 收到认证成功即标记本次连接已认证，Run 据此重置退避（P5）
func TestHandleMessage_AuthSuccessMarksConnAuthed(t *testing.T) {
	d := newTestDaemon(t, t.TempDir(), nil)
	msg, _ := ws.NewMessage(ws.MsgTypeAuthResult, &ws.AuthResponse{Success: true})
	if err := d.handleMessage(msg); err != nil {
		t.Fatalf("handleMessage() error = %v", err)
	}
	if !d.connAuthed {
		t.Fatal("认证成功后 connAuthed 应为 true")
	}
}

// 通配订阅同步时应上报工作目录中匹配的本地域名时间戳（与服务端匹配规则一致）
func TestCollectLocalTimestamps_WildcardSubscription(t *testing.T) {
	workDir := t.TempDir()
	for domain, ts := range map[string]string{"a.example.com": "100", "other.org": "50"} {
		dir := filepath.Join(workDir, domain)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "time.log"), []byte(ts), 0644); err != nil {
			t.Fatal(err)
		}
	}

	got := collectLocalTimestamps(workDir, []string{"*.example.com"})

	if got["a.example.com"] != 100 {
		t.Errorf("a.example.com 时间戳 = %d, want 100（全部: %v）", got["a.example.com"], got)
	}
	if _, ok := got["*.example.com"]; !ok {
		t.Errorf("字面订阅项 *.example.com 应照常上报（全部: %v）", got)
	}
	if _, ok := got["other.org"]; ok {
		t.Errorf("不匹配的 other.org 不应上报（全部: %v）", got)
	}
}
