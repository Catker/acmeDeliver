package client

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Catker/acmeDeliver/pkg/config"
)

// testCertFiles 构造内存中的三份证书源文件
func testCertFiles(cert, key, fullchain string) map[string][]byte {
	return map[string][]byte{
		"cert.pem":      []byte(cert),
		"key.pem":       []byte(key),
		"fullchain.pem": []byte(fullchain),
	}
}

// testPEM 测试用真实证书材料：cert 与 key 配对，chain 为 cert 后接一张无关证书
type testPEM struct{ cert, key, chain string }

var (
	validPEM = mustTestPEM()
	otherPEM = mustTestPEM() // 与 validPEM 不配对，用于构造私钥不匹配
)

// validCertFiles 返回通过 validateKeyPair 的三份证书源文件
func validCertFiles() map[string][]byte {
	return testCertFiles(validPEM.cert, validPEM.key, validPEM.chain)
}

func mustTestPEM() testPEM {
	key := mustECKey()
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		panic(err)
	}
	cert := selfSignedPEM(key)
	return testPEM{
		cert:  cert,
		key:   string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})),
		chain: cert + selfSignedPEM(mustECKey()),
	}
}

func mustECKey() *ecdsa.PrivateKey {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	return key
}

func selfSignedPEM(key *ecdsa.PrivateKey) string {
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "example.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestDeploySite_WritesContentAndPerms(t *testing.T) {
	dstDir := t.TempDir()
	site := &config.SiteDeployConfig{
		CertPath:      filepath.Join(dstDir, "{domain}", "cert.pem"),
		KeyPath:       filepath.Join(dstDir, "{domain}", "key.pem"),
		FullchainPath: filepath.Join(dstDir, "{domain}", "fullchain.pem"),
	}

	if err := DeploySite(site, "example.com", testCertFiles("cert-data", "key-data", "chain-data")); err != nil {
		t.Fatalf("DeploySite() error = %v", err)
	}

	// {domain} 占位符替换 + 内容 + 权限（key.pem 0600，其余 0644）
	assertFileContentPerm(t, filepath.Join(dstDir, "example.com", "cert.pem"), "cert-data", 0644)
	assertFileContentPerm(t, filepath.Join(dstDir, "example.com", "key.pem"), "key-data", 0600)
	assertFileContentPerm(t, filepath.Join(dstDir, "example.com", "fullchain.pem"), "chain-data", 0644)
}

// 工作目录中预置的软链接不得被跟随：被链接文件不变，key.pem 变为普通文件（0600）
func TestApplyCert_WorkDirDoesNotFollowSymlink(t *testing.T) {
	workDir := t.TempDir()
	domainDir := filepath.Join(workDir, "example.com")
	if err := os.MkdirAll(domainDir, 0755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "victim.txt")
	if err := os.WriteFile(victim, []byte("victim-data"), 0644); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(domainDir, "key.pem")
	if err := os.Symlink(victim, keyPath); err != nil {
		t.Fatal(err)
	}

	if err := ApplyCert(workDir, "example.com", testCertFiles("cert-data", "new-key", "chain-data"), nil); err != nil {
		t.Fatalf("ApplyCert() error = %v", err)
	}

	assertFileContentPerm(t, victim, "victim-data", 0644)
	info, err := os.Lstat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatal("工作目录 key.pem 应被替换为普通文件")
	}
	assertFileContentPerm(t, keyPath, "new-key", 0600)
}

func TestDeploySite_PartialConfig(t *testing.T) {
	dstDir := t.TempDir()
	site := &config.SiteDeployConfig{CertPath: filepath.Join(dstDir, "cert.pem")}

	// 未配置的 key/fullchain 即使源内容为空也不影响部署
	if err := DeploySite(site, "example.com", map[string][]byte{"cert.pem": []byte("cert only")}); err != nil {
		t.Fatalf("DeploySite() error = %v", err)
	}
	assertFileContentPerm(t, filepath.Join(dstDir, "cert.pem"), "cert only", 0644)
	if _, err := os.Stat(filepath.Join(dstDir, "key.pem")); !os.IsNotExist(err) {
		t.Error("未配置 key_path 时不应写入 key.pem")
	}
}

// 空源文件（缺失或空内容）必须整体拒绝部署，不得部分写入或清空已有目标
func TestDeploySite_EmptySourceRejectedWithoutPartialWrite(t *testing.T) {
	for _, emptyName := range []string{"cert.pem", "key.pem", "fullchain.pem"} {
		t.Run(emptyName, func(t *testing.T) {
			files := testCertFiles("cert-data", "key-data", "chain-data")
			files[emptyName] = []byte{}

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
				CertPath:      filepath.Join(dstDir, "cert.pem"),
				KeyPath:       filepath.Join(dstDir, "key.pem"),
				FullchainPath: filepath.Join(dstDir, "fullchain.pem"),
			}

			err := DeploySite(site, "example.com", files)
			if err == nil || !strings.Contains(err.Error(), emptyName) {
				t.Fatalf("源文件 %s 为空时应返回指明该文件的错误，得到 %v", emptyName, err)
			}
			for name, want := range sentinel {
				assertFileContentPerm(t, filepath.Join(dstDir, name), want, 0644)
			}
		})
	}
}

func TestReadLocalTimestamp(t *testing.T) {
	workDir := t.TempDir()
	if got := ReadLocalTimestamp(workDir, "example.com"); got != 0 {
		t.Fatalf("time.log 不存在时应返回 0，得到 %d", got)
	}
	if err := os.MkdirAll(filepath.Join(workDir, "example.com"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "example.com", "time.log"), []byte("1757011200\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := ReadLocalTimestamp(workDir, "example.com"); got != 1757011200 {
		t.Fatalf("ReadLocalTimestamp() = %d, want 1757011200", got)
	}
}

func TestIsCertUpToDate(t *testing.T) {
	tests := []struct {
		name     string
		localTS  int64
		serverTS int64
		want     bool
	}{
		{"本地与服务端相同则跳过", 100, 100, true},
		{"本地较新则跳过", 200, 100, true},
		{"本地较旧需更新", 99, 100, false},
		{"本地无 time.log 需更新", 0, 100, false},
		{"服务端无时间戳需更新", 100, 0, false},
		{"双方都无时间戳需更新", 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsCertUpToDate(tt.localTS, tt.serverTS); got != tt.want {
				t.Errorf("IsCertUpToDate(%d, %d) = %v, want %v", tt.localTS, tt.serverTS, got, tt.want)
			}
		})
	}
}

// ReceiveCert：时间戳比较 → reload 命令选择 → （非 DryRun）ApplyCert
func TestReceiveCert_Shared(t *testing.T) {
	const serverTS = 1757011200
	pushFiles := func() map[string][]byte {
		files := validCertFiles()
		files["time.log"] = []byte("1757011200\n")
		return files
	}

	tests := []struct {
		name        string
		localTS     string // 预置的本地 time.log，空表示不存在
		site        func(t *testing.T) *config.SiteDeployConfig
		opts        ReceiveOptions
		wantCmd     string
		wantErr     string // 非空时要求错误包含该子串
		wantWritten bool   // 工作目录 cert.pem 是否被写入
		wantTimeLog bool   // 工作目录 time.log 是否为新值
	}{
		{
			name:    "本地不旧时跳过且不写盘",
			localTS: "1757011200\n",
			site:    func(t *testing.T) *config.SiteDeployConfig { return &config.SiteDeployConfig{ReloadCmd: "site-reload"} },
			opts:    ReceiveOptions{DefaultReloadCmd: "default-reload"},
		},
		{
			name:        "Force 时即使不旧也应用",
			localTS:     "1757011200\n",
			site:        func(t *testing.T) *config.SiteDeployConfig { return &config.SiteDeployConfig{ReloadCmd: "site-reload"} },
			opts:        ReceiveOptions{Force: true},
			wantCmd:     "site-reload",
			wantWritten: true,
			wantTimeLog: true,
		},
		{
			name:    "DryRun 不写任何文件但返回 reload 命令",
			site:    func(t *testing.T) *config.SiteDeployConfig { return &config.SiteDeployConfig{} },
			opts:    ReceiveOptions{DryRun: true, DefaultReloadCmd: "default-reload"},
			wantCmd: "default-reload",
		},
		{
			name:        "site 为 nil 时写工作目录并使用 default",
			site:        func(t *testing.T) *config.SiteDeployConfig { return nil },
			opts:        ReceiveOptions{DefaultReloadCmd: "default-reload"},
			wantCmd:     "default-reload",
			wantWritten: true,
			wantTimeLog: true,
		},
		{
			name:        "site 为 nil 时 override 优先于 default",
			site:        func(t *testing.T) *config.SiteDeployConfig { return nil },
			opts:        ReceiveOptions{ReloadOverride: "override", DefaultReloadCmd: "default-reload"},
			wantCmd:     "override",
			wantWritten: true,
			wantTimeLog: true,
		},
		{
			name:        "site 为 nil 且无 default 时 reload 为空",
			site:        func(t *testing.T) *config.SiteDeployConfig { return nil },
			wantWritten: true,
			wantTimeLog: true,
		},
		{
			name:        "reload 优先级：override 优先于 site",
			site:        func(t *testing.T) *config.SiteDeployConfig { return &config.SiteDeployConfig{ReloadCmd: "site-reload"} },
			opts:        ReceiveOptions{ReloadOverride: "override", DefaultReloadCmd: "default-reload"},
			wantCmd:     "override",
			wantWritten: true,
			wantTimeLog: true,
		},
		{
			name:        "reload 优先级：site 优先于 default",
			localTS:     "1757011199\n",
			site:        func(t *testing.T) *config.SiteDeployConfig { return &config.SiteDeployConfig{ReloadCmd: "site-reload"} },
			opts:        ReceiveOptions{DefaultReloadCmd: "default-reload"},
			wantCmd:     "site-reload",
			wantWritten: true,
			wantTimeLog: true,
		},
		{
			name:        "reload 优先级：site 未配置时用 default",
			site:        func(t *testing.T) *config.SiteDeployConfig { return &config.SiteDeployConfig{} },
			opts:        ReceiveOptions{DefaultReloadCmd: "default-reload"},
			wantCmd:     "default-reload",
			wantWritten: true,
			wantTimeLog: true,
		},
		{
			name: "部署失败时返回错误且不写 time.log",
			site: func(t *testing.T) *config.SiteDeployConfig {
				// 目标路径是目录，原子写入必然失败
				return &config.SiteDeployConfig{CertPath: t.TempDir(), ReloadCmd: "site-reload"}
			},
			wantErr:     "部署证书失败",
			wantWritten: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workDir := t.TempDir()
			domainDir := filepath.Join(workDir, "example.com")
			if tt.localTS != "" {
				if err := os.MkdirAll(domainDir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(domainDir, "time.log"), []byte(tt.localTS), 0644); err != nil {
					t.Fatal(err)
				}
			}

			cmd, err := ReceiveCert(workDir, "example.com", pushFiles(), serverTS, tt.site(t), tt.opts)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want 包含 %q", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatalf("ReceiveCert() error = %v", err)
			}
			if cmd != tt.wantCmd {
				t.Errorf("ReceiveCert() cmd = %q, want %q", cmd, tt.wantCmd)
			}

			if _, err := os.Stat(filepath.Join(domainDir, "cert.pem")); (err == nil) != tt.wantWritten {
				t.Errorf("cert.pem 写入状态 = %v, want %v", err == nil, tt.wantWritten)
			}
			gotTS := ReadLocalTimestamp(workDir, "example.com")
			switch {
			case tt.wantTimeLog && gotTS != serverTS:
				t.Errorf("time.log = %d, want %d", gotTS, serverTS)
			case !tt.wantTimeLog && tt.localTS == "" && gotTS != 0:
				t.Errorf("不应写入 time.log，得到 %d", gotTS)
			}
		})
	}
}

// 证书与私钥无法配对时拒绝，且不写工作目录（DryRun 同样拒绝）
func TestReceiveCert_RejectsInvalidKeyPair(t *testing.T) {
	tests := []struct {
		name  string
		files map[string][]byte
		opts  ReceiveOptions
	}{
		{"缺少 key.pem", map[string][]byte{"cert.pem": []byte(validPEM.cert), "fullchain.pem": []byte(validPEM.chain)}, ReceiveOptions{}},
		{"只有 key.pem", map[string][]byte{"key.pem": []byte(validPEM.key)}, ReceiveOptions{}},
		{"私钥与证书不匹配", testCertFiles(validPEM.cert, otherPEM.key, validPEM.chain), ReceiveOptions{}},
		{"fullchain 与私钥不匹配", testCertFiles(validPEM.cert, validPEM.key, otherPEM.chain), ReceiveOptions{}},
		{"证书内容损坏", testCertFiles("garbage", validPEM.key, validPEM.chain), ReceiveOptions{}},
		{"DryRun 同样校验", testCertFiles(validPEM.cert, otherPEM.key, validPEM.chain), ReceiveOptions{DryRun: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workDir := t.TempDir()
			site := &config.SiteDeployConfig{ReloadCmd: "site-reload"}
			cmd, err := ReceiveCert(workDir, "example.com", tt.files, 1757011200, site, tt.opts)
			if err == nil || !strings.Contains(err.Error(), "证书校验失败") {
				t.Fatalf("err = %v, want 包含 %q", err, "证书校验失败")
			}
			if cmd != "" {
				t.Errorf("校验失败时不应返回 reload 命令，得到 %q", cmd)
			}
			if _, err := os.Stat(filepath.Join(workDir, "example.com")); !os.IsNotExist(err) {
				t.Errorf("校验失败时不应写工作目录，stat err = %v", err)
			}
		})
	}
}
