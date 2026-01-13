package security

import (
	"testing"
	"time"
)

func TestSignatureVerifier_GenerateSignature(t *testing.T) {
	verifier := NewSignatureVerifier("testpassword")
	timestamp := int64(1234567890)

	sig1 := verifier.GenerateSignature(timestamp)
	sig2 := verifier.GenerateSignature(timestamp)

	// 相同输入应产生相同签名
	if sig1 != sig2 {
		t.Error("相同输入应产生相同签名")
	}

	// 不同时间戳应产生不同签名
	sig3 := verifier.GenerateSignature(timestamp + 1)
	if sig1 == sig3 {
		t.Error("不同时间戳应产生不同签名")
	}

	// 不同密码应产生不同签名
	verifier2 := NewSignatureVerifier("differentpassword")
	sig4 := verifier2.GenerateSignature(timestamp)
	if sig1 == sig4 {
		t.Error("不同密码应产生不同签名")
	}
}

func TestSignatureVerifier_VerifySignature(t *testing.T) {
	password := "testpassword"
	verifier := NewSignatureVerifier(password)

	// 生成当前时间的签名
	now := time.Now().Unix()
	validSig := verifier.GenerateSignature(now)

	tests := []struct {
		name      string
		signature string
		timestamp int64
		wantOk    bool
		wantErr   string
	}{
		{
			name:      "有效签名",
			signature: validSig,
			timestamp: now,
			wantOk:    true,
			wantErr:   "",
		},
		{
			name:      "错误签名",
			signature: "invalidsignature",
			timestamp: now,
			wantOk:    false,
			wantErr:   "签名验证失败",
		},
		{
			name:      "过期时间戳",
			signature: verifier.GenerateSignature(now - 60),
			timestamp: now - 60,
			wantOk:    false,
			wantErr:   "时间戳已过期",
		},
		{
			name:      "未来时间戳",
			signature: verifier.GenerateSignature(now + 60),
			timestamp: now + 60,
			wantOk:    false,
			wantErr:   "时间戳已过期",
		},
		{
			name:      "边界容差内",
			signature: verifier.GenerateSignature(now - 29),
			timestamp: now - 29,
			wantOk:    true,
			wantErr:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, errMsg := verifier.VerifySignature(tt.signature, tt.timestamp)
			if ok != tt.wantOk {
				t.Errorf("VerifySignature() ok = %v, want %v", ok, tt.wantOk)
			}
			if errMsg != tt.wantErr {
				t.Errorf("VerifySignature() errMsg = %v, want %v", errMsg, tt.wantErr)
			}
		})
	}
}

func TestSignatureVerifier_CustomTolerance(t *testing.T) {
	password := "testpassword"
	tolerance := int64(5)
	verifier := NewSignatureVerifierWithTolerance(password, tolerance)

	now := time.Now().Unix()

	// 在自定义容差内应该通过
	sig := verifier.GenerateSignature(now - 4)
	ok, _ := verifier.VerifySignature(sig, now-4)
	if !ok {
		t.Error("在容差范围内应该验证通过")
	}

	// 超出自定义容差应该失败
	sig = verifier.GenerateSignature(now - 10)
	ok, errMsg := verifier.VerifySignature(sig, now-10)
	if ok {
		t.Error("超出容差范围应该验证失败")
	}
	if errMsg != "时间戳已过期" {
		t.Errorf("期望 '时间戳已过期' 错误，得到 %v", errMsg)
	}
}
