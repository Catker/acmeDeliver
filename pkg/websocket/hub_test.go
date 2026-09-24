package websocket

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Catker/acmeDeliver/pkg/security"
)

// 广播与注销并发执行时不得向已关闭的 send 通道发送（B2）
func TestBroadcastCert_ConcurrentUnregisterNoPanic(t *testing.T) {
	hub := NewHub()

	const n = 50
	clients := make([]*Client, n)
	for i := range clients {
		c := &Client{hub: hub, send: make(chan []byte, 256), domains: []string{"example.com"}}
		clients[i] = c
		hub.Register(c)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			hub.BroadcastCert("example.com", &CertPushData{Domain: "example.com"})
		}
	}()
	go func() {
		defer wg.Done()
		for _, c := range clients {
			hub.Unregister(c)
		}
	}()
	wg.Wait()
}

// newTestServerConn 建立一对真实 WebSocket 连接，返回服务端一侧连接
func newTestServerConn(t *testing.T) *websocket.Conn {
	t.Helper()
	conns := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		conns <- c
	}))
	t.Cleanup(srv.Close)

	cc, _, err := websocket.DefaultDialer.Dial("ws://"+srv.Listener.Addr().String(), nil)
	if err != nil {
		t.Fatalf("连接测试服务器失败: %v", err)
	}
	t.Cleanup(func() { cc.Close() })

	sc := <-conns
	t.Cleanup(func() { sc.Close() })
	return sc
}

// 已认证连接重复 auth 不得重复注册，断开后不能残留旧订阅（B3）
func TestHandleAuth_DuplicateAuthLeavesNoStaleSubscription(t *testing.T) {
	hub := NewHub()

	verifier := security.NewSignatureVerifier("secret")
	client := &Client{hub: hub, conn: newTestServerConn(t), send: make(chan []byte, 256), verifier: verifier}

	newAuthMsg := func(domains []string) *Message {
		ts := time.Now().Unix()
		msg, err := NewMessage(MsgTypeAuth, &AuthRequest{
			ClientID:  "c1",
			Signature: verifier.GenerateSignature(ts),
			Domains:   domains,
		})
		if err != nil {
			t.Fatal(err)
		}
		msg.Timestamp = ts
		return msg
	}

	if !client.handleAuth(newAuthMsg([]string{"a.com"})) {
		t.Fatal("首次认证应成功")
	}
	client.handleAuth(newAuthMsg([]string{"b.com"}))

	hub.Unregister(client)

	hub.mu.RLock()
	stale := len(hub.subscriptions)
	hub.mu.RUnlock()
	if stale != 0 {
		t.Fatalf("注销后不应残留订阅，got %d 个域名", stale)
	}
	if sent := hub.BroadcastCert("a.com", &CertPushData{Domain: "a.com"}); sent != 0 {
		t.Fatalf("注销后不应向旧客户端推送，sent = %d", sent)
	}
}

// 认证失败：失败结果经 writePump 写出后再关闭连接（所有写都经 send 通道）
func TestServeWs_AuthFailureWritesResultThenCloses(t *testing.T) {
	hub := NewHub()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ServeWs(hub, "secret", t.TempDir(), security.NewIPWhitelist(""), false, w, r)
	}))
	defer srv.Close()

	cc, _, err := websocket.DefaultDialer.Dial("ws://"+srv.Listener.Addr().String(), nil)
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	defer cc.Close()

	msg, _ := NewMessage(MsgTypeAuth, &AuthRequest{ClientID: "c1", Signature: "bad"})
	data, _ := json.Marshal(msg)
	if err := cc.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatal(err)
	}

	cc.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, resp, err := cc.ReadMessage()
	if err != nil {
		t.Fatalf("应先收到认证结果，got err %v", err)
	}
	var got Message
	var auth AuthResponse
	if err := json.Unmarshal(resp, &got); err != nil || got.Type != MsgTypeAuthResult {
		t.Fatalf("应收到 auth_result，got %s", resp)
	}
	if err := got.ParseData(&auth); err != nil || auth.Success {
		t.Fatalf("认证应失败，got %+v", auth)
	}
	if _, _, err := cc.ReadMessage(); err == nil {
		t.Fatal("认证失败后服务端应关闭连接")
	}
}

// 未认证连接超过 authWait 应被断开，服务端 ping/客户端 pong 不能续期
func TestServeWs_UnauthenticatedConnTimesOut(t *testing.T) {
	old := authWait
	authWait = 200 * time.Millisecond
	t.Cleanup(func() { authWait = old })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ServeWs(NewHub(), "secret", t.TempDir(), security.NewIPWhitelist(""), false, w, r)
	}))
	defer srv.Close()

	cc, _, err := websocket.DefaultDialer.Dial("ws://"+srv.Listener.Addr().String(), nil)
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	defer cc.Close()

	// 主动发 pong 模拟保活，不应延长未认证连接的时限
	cc.WriteControl(websocket.PongMessage, nil, time.Now().Add(time.Second))

	cc.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, _, err := cc.ReadMessage(); err == nil || isTimeout(err) {
		t.Fatalf("未认证连接应在 authWait 后被服务端关闭，got %v", err)
	}
}

func isTimeout(err error) bool {
	ne, ok := err.(interface{ Timeout() bool })
	return ok && ne.Timeout()
}

// dialTestServer 启动使用 ServeWs 的测试服务器并建立连接
func dialTestServer(t *testing.T, password string) *websocket.Conn {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ServeWs(NewHub(), password, t.TempDir(), security.NewIPWhitelist(""), false, w, r)
	}))
	t.Cleanup(srv.Close)

	cc, _, err := websocket.DefaultDialer.Dial("ws://"+srv.Listener.Addr().String(), nil)
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	t.Cleanup(func() { cc.Close() })
	return cc
}

// writeTestMessage 序列化并发送消息
func writeTestMessage(t *testing.T, cc *websocket.Conn, msg *Message) {
	t.Helper()
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	if err := cc.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatal(err)
	}
}

// readTestMessage 读取下一条消息，要求类型为 wantType
func readTestMessage(t *testing.T, cc *websocket.Conn, wantType string) *Message {
	t.Helper()
	cc.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err := cc.ReadMessage()
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", wantType, err)
	}
	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil || msg.Type != wantType {
		t.Fatalf("应收到 %s，got %s", wantType, data)
	}
	return &msg
}

// 未认证连接发送超过 authMaxMessageSize 的消息应被断开
func TestServeWs_UnauthenticatedOversizedMessageCloses(t *testing.T) {
	cc := dialTestServer(t, "secret")

	big := make([]byte, authMaxMessageSize+1)
	for i := range big {
		big[i] = 'a'
	}
	if err := cc.WriteMessage(websocket.TextMessage, big); err != nil {
		t.Fatal(err)
	}

	cc.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, _, err := cc.ReadMessage(); err == nil || isTimeout(err) {
		t.Fatalf("超限的未认证消息应导致服务端关闭连接，got %v", err)
	}
}

// 认证成功后恢复 maxMessageSize：可发送超过 authMaxMessageSize 的 sync_request，连接保持可用
func TestServeWs_AuthenticatedAllowsLargeMessage(t *testing.T) {
	verifier := security.NewSignatureVerifier("secret")
	cc := dialTestServer(t, "secret")

	ts := time.Now().Unix()
	auth, _ := NewMessage(MsgTypeAuth, &AuthRequest{ClientID: "c1", Signature: verifier.GenerateSignature(ts)})
	auth.Timestamp = ts
	writeTestMessage(t, cc, auth)

	var authResp AuthResponse
	if err := readTestMessage(t, cc, MsgTypeAuthResult).ParseData(&authResp); err != nil || !authResp.Success {
		t.Fatalf("认证应成功，got %+v", authResp)
	}

	timestamps := make(map[string]int64)
	for i := 0; i < 5000; i++ {
		timestamps[strings.Repeat("x", 20)+strconv.Itoa(i)+".example.com"] = 0
	}
	sync, _ := NewMessage(MsgTypeSyncRequest, &SyncRequest{Timestamps: timestamps})
	if len(sync.Data) <= authMaxMessageSize {
		t.Fatalf("测试消息应超过 authMaxMessageSize，got %d", len(sync.Data))
	}
	writeTestMessage(t, cc, sync)

	// 连接仍可用：状态请求正常响应
	status, _ := NewMessage(MsgTypeStatusRequest, &StatusRequest{})
	writeTestMessage(t, cc, status)
	readTestMessage(t, cc, MsgTypeStatusResponse)
}

// 浏览器跨源请求（Origin 与 Host 不同）应被拒绝，防止 CSWSH；无 Origin 的客户端不受影响（见其他用例）
func TestServeWs_RejectsCrossOrigin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ServeWs(NewHub(), "secret", t.TempDir(), security.NewIPWhitelist(""), false, w, r)
	}))
	defer srv.Close()

	header := http.Header{"Origin": []string{"https://evil.example"}}
	cc, resp, err := websocket.DefaultDialer.Dial("ws://"+srv.Listener.Addr().String(), header)
	if err == nil {
		cc.Close()
		t.Fatal("跨源请求应被拒绝")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("跨源请求应返回 403，got resp=%v err=%v", resp, err)
	}
}
