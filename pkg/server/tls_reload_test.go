package server

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeSelfSignedPair 生成自签证书写入 certFile/keyFile，返回证书 DER
func writeSelfSignedPair(t *testing.T, certFile, keyFile, cn string) []byte {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		t.Fatal(err)
	}
	return der
}

// 证书文件替换（mtime 变化）后 GetCertificate 应返回新证书
func TestCertReloader_ReloadsOnMtimeChange(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "cert.pem")
	keyFile := filepath.Join(dir, "key.pem")

	derA := writeSelfSignedPair(t, certFile, keyFile, "a.example.com")
	r, err := newCertReloader(certFile, keyFile)
	if err != nil {
		t.Fatalf("newCertReloader() error = %v", err)
	}
	got, err := r.GetCertificate(nil)
	if err != nil || !bytes.Equal(got.Certificate[0], derA) {
		t.Fatalf("初始应返回证书 A, err = %v", err)
	}

	derB := writeSelfSignedPair(t, certFile, keyFile, "b.example.com")
	future := time.Now().Add(time.Minute)
	for _, f := range []string{certFile, keyFile} {
		if err := os.Chtimes(f, future, future); err != nil {
			t.Fatal(err)
		}
	}
	got, err = r.GetCertificate(nil)
	if err != nil || !bytes.Equal(got.Certificate[0], derB) {
		t.Fatalf("替换后应返回证书 B, err = %v", err)
	}

	// 写入损坏内容后继续使用旧证书 B
	if err := os.WriteFile(certFile, []byte("broken"), 0644); err != nil {
		t.Fatal(err)
	}
	later := future.Add(time.Minute)
	if err := os.Chtimes(certFile, later, later); err != nil {
		t.Fatal(err)
	}
	got, err = r.GetCertificate(nil)
	if err != nil || !bytes.Equal(got.Certificate[0], derB) {
		t.Fatalf("加载失败时应继续返回证书 B, err = %v", err)
	}
}

// 启动时证书不可用应返回错误
func TestNewCertReloader_MissingFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := newCertReloader(filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")); err == nil {
		t.Fatal("证书文件不存在时应返回错误")
	}
}
