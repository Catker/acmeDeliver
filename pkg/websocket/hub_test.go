package websocket

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
