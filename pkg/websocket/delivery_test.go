package websocket

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/Catker/acmeDeliver/pkg/cert"
)

// newAuthedTestClient 构造已认证并注册到 hub 的测试客户端
func newAuthedTestClient(hub *Hub, id string) *Client {
	c := &Client{ID: id, hub: hub, send: make(chan []byte, 256), authenticated: true, certs: cert.NewStore("")}
	hub.Register(c)
	return c
}

func sendAck(t *testing.T, c *Client, ack CertAck) {
	t.Helper()
	msg, err := NewMessage(MsgTypeCertAck, &ack)
	if err != nil {
		t.Fatal(err)
	}
	c.handleMessage(msg)
}

// cert_ack 应按域名记录最近一次结果，并出现在 GetClientStatus 与状态响应中
func TestCertAck_RecordedInStatus(t *testing.T) {
	hub := NewHub()
	c := newAuthedTestClient(hub, "node-1")

	sendAck(t, c, CertAck{Domain: "b.example.com", Success: false, Message: "部署失败"})
	sendAck(t, c, CertAck{Domain: "a.example.com", Success: false, Message: "旧错误", Timestamp: 100})
	sendAck(t, c, CertAck{Domain: "a.example.com", Success: true, Timestamp: 200})
	sendAck(t, c, CertAck{Success: true}) // 无域名的 ACK 忽略

	status := hub.GetClientStatus()
	if len(status) != 1 {
		t.Fatalf("在线客户端数 = %d, want 1", len(status))
	}
	got := status[0].Deliveries
	if len(got) != 2 {
		t.Fatalf("交付记录数 = %d, want 2: %+v", len(got), got)
	}
	if got[0].Domain != "a.example.com" || !got[0].Success || got[0].Message != "" || got[0].Timestamp != 200 || got[0].AckedAt == 0 {
		t.Errorf("同域名应取最新 ACK，得到 %+v", got[0])
	}
	if got[1].Domain != "b.example.com" || got[1].Success || got[1].Message != "部署失败" {
		t.Errorf("失败 ACK 记录不符，得到 %+v", got[1])
	}

	// 状态响应带出交付记录
	req, _ := NewMessage(MsgTypeStatusRequest, nil)
	c.handleMessage(req)
	var msg Message
	if err := json.Unmarshal(<-c.send, &msg); err != nil {
		t.Fatal(err)
	}
	var resp StatusResponse
	if err := msg.ParseData(&resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Clients) != 1 || len(resp.Clients[0].Deliveries) != 2 || resp.Clients[0].Deliveries[0].Timestamp != 200 {
		t.Errorf("状态响应未带出交付记录: %+v", resp.Clients)
	}
}

// 并发 ACK 与状态查询不得产生数据竞争（配合 -race）
func TestCertAck_ConcurrentWithStatus(t *testing.T) {
	hub := NewHub()
	c := newAuthedTestClient(hub, "node-1")

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			hub.RecordAck(c, &CertAck{Domain: "example.com", Success: i%2 == 0, Timestamp: int64(i)})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			for _, s := range hub.GetClientStatus() {
				_ = len(s.Deliveries)
			}
		}
	}()
	wg.Wait()

	if d := hub.GetClientStatus()[0].Deliveries; len(d) != 1 || d[0].Timestamp != 199 {
		t.Errorf("最终交付记录应为最后一次 ACK，得到 %+v", d)
	}
}
