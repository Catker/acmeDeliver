package websocket

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeDomainCert 在 baseDir 下创建域名目录并写入 cert.pem 与 time.log
func writeDomainCert(t *testing.T, baseDir, domain, ts string) {
	t.Helper()
	dir := filepath.Join(baseDir, domain)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cert.pem"), []byte("cert"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "time.log"), []byte(ts), 0644); err != nil {
		t.Fatal(err)
	}
}

// drainPushedDomains 取出 send 中已排队的证书推送域名
func drainPushedDomains(t *testing.T, c *Client) []string {
	t.Helper()
	var domains []string
	for {
		select {
		case data := <-c.send:
			var msg Message
			if err := json.Unmarshal(data, &msg); err != nil {
				t.Fatal(err)
			}
			if msg.Type != MsgTypeCertPush {
				t.Fatalf("意外的消息类型: %s", msg.Type)
			}
			var push CertPushData
			if err := msg.ParseData(&push); err != nil {
				t.Fatal(err)
			}
			domains = append(domains, push.Domain)
		default:
			return domains
		}
	}
}

// 通配订阅 "*.example.com" 重连同步时应按时间戳补推匹配的子域名，不推送不匹配的域名
func TestHandleSyncRequest_WildcardSubscription(t *testing.T) {
	baseDir := t.TempDir()
	writeDomainCert(t, baseDir, "a.example.com", "200")
	writeDomainCert(t, baseDir, "other.org", "300")

	tests := []struct {
		name     string
		domains  []string
		clientTS int64
		want     []string
	}{
		{"服务端较新时推送", []string{"*.example.com"}, 100, []string{"a.example.com"}},
		{"客户端已是最新时不推送", []string{"*.example.com"}, 200, nil},
		// 同一域名命中多个订阅项只推送一次
		{"多订阅项命中只推送一次", []string{"*.example.com", "a.example.com"}, 100, []string{"a.example.com"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Client{
				send:    make(chan []byte, 16),
				domains: tt.domains,
				baseDir: baseDir,
			}
			msg, err := NewMessage(MsgTypeSyncRequest, &SyncRequest{
				Timestamps: map[string]int64{"a.example.com": tt.clientTS},
			})
			if err != nil {
				t.Fatal(err)
			}

			c.handleSyncRequest(msg)

			got := drainPushedDomains(t, c)
			if len(got) != len(tt.want) || (len(got) == 1 && got[0] != tt.want[0]) {
				t.Errorf("推送域名 = %v, want %v", got, tt.want)
			}
		})
	}
}

// 通配订阅同时匹配 acme.sh 通配证书的字面同名目录（"*.example.com"）
func TestHandleSyncRequest_WildcardLiteralDir(t *testing.T) {
	baseDir := t.TempDir()
	writeDomainCert(t, baseDir, "*.example.com", "200")

	c := &Client{send: make(chan []byte, 16), domains: []string{"*.example.com"}, baseDir: baseDir}
	msg, err := NewMessage(MsgTypeSyncRequest, &SyncRequest{Timestamps: map[string]int64{}})
	if err != nil {
		t.Fatal(err)
	}

	c.handleSyncRequest(msg)

	got := drainPushedDomains(t, c)
	if len(got) != 1 || got[0] != "*.example.com" {
		t.Errorf("推送域名 = %v, want [*.example.com]", got)
	}
}
