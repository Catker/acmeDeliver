// Package security 提供安全相关的功能，包括签名验证和 IP 白名单
package security

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strconv"
	"time"
)

const (
	// DefaultTimestampTolerance 默认时间戳容差（秒）
	DefaultTimestampTolerance int64 = 30
)

// SignatureVerifier 签名验证器
type SignatureVerifier struct {
	password           string
	timestampTolerance int64
}

// NewSignatureVerifier 创建签名验证器
func NewSignatureVerifier(password string) *SignatureVerifier {
	return &SignatureVerifier{
		password:           password,
		timestampTolerance: DefaultTimestampTolerance,
	}
}

// NewSignatureVerifierWithTolerance 创建自定义时间容差的签名验证器
func NewSignatureVerifierWithTolerance(password string, tolerance int64) *SignatureVerifier {
	return &SignatureVerifier{
		password:           password,
		timestampTolerance: tolerance,
	}
}

// GenerateSignature 生成签名: sha256(password + timestamp)
func (v *SignatureVerifier) GenerateSignature(timestamp int64) string {
	timestampStr := strconv.FormatInt(timestamp, 10)
	hash := sha256.Sum256([]byte(v.password + timestampStr))
	return hex.EncodeToString(hash[:])
}

// VerifySignature 验证签名
// 返回值: 是否验证通过, 错误描述（如果失败）
func (v *SignatureVerifier) VerifySignature(signature string, timestamp int64) (bool, string) {
	// 检查时间戳是否在容差范围内
	now := time.Now().Unix()
	if timestamp < now-v.timestampTolerance || timestamp > now+v.timestampTolerance {
		return false, "时间戳已过期"
	}

	// 生成预期签名
	expectedSig := v.GenerateSignature(timestamp)

	// 使用恒定时间比较防止时序攻击
	if subtle.ConstantTimeCompare([]byte(signature), []byte(expectedSig)) != 1 {
		return false, "签名验证失败"
	}

	return true, ""
}
