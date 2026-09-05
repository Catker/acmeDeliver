package deployer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Catker/acmeDeliver/pkg/client"
)

// 合并原 TestNewDeployer_NoOpDeployer / TestNewDeployer_ConfigDrivenDeployer /
// TestNoOpDeployer_Deploy：实现选择与 NoOp 行为是同一输入输出关系
func TestNewDeployer(t *testing.T) {
	certs := &client.CertificateFiles{
		Cert: []byte("cert content"),
		Key:  []byte("key content"),
	}

	tests := []struct {
		name string
		cfg  DeploymentConfig
		want Deployer
	}{
		{
			name: "未配置任何路径返回 NoOpDeployer",
			cfg:  DeploymentConfig{Domain: "example.com"},
			want: &NoOpDeployer{},
		},
		{
			name: "仅 cert_path 返回 ConfigDrivenDeployer",
			cfg:  DeploymentConfig{CertPath: "/cert.pem"},
			want: &ConfigDrivenDeployer{},
		},
		{
			name: "仅 key_path 返回 ConfigDrivenDeployer",
			cfg:  DeploymentConfig{KeyPath: "/key.pem"},
			want: &ConfigDrivenDeployer{},
		},
		{
			name: "仅 fullchain_path 返回 ConfigDrivenDeployer",
			cfg:  DeploymentConfig{FullchainPath: "/fullchain.pem"},
			want: &ConfigDrivenDeployer{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewDeployer(tt.cfg)
			if got == nil {
				t.Fatal("NewDeployer() = nil")
			}
			switch tt.want.(type) {
			case *NoOpDeployer:
				if _, ok := got.(*NoOpDeployer); !ok {
					t.Errorf("NewDeployer() = %T, want *NoOpDeployer", got)
				}
				// NoOp 部署器不应做任何事，也不应报错
				if err := got.Deploy(certs, false); err != nil {
					t.Errorf("NoOpDeployer.Deploy() error = %v, want nil", err)
				}
				if err := got.Deploy(certs, true); err != nil {
					t.Errorf("NoOpDeployer.Deploy() dryRun error = %v, want nil", err)
				}
			case *ConfigDrivenDeployer:
				if _, ok := got.(*ConfigDrivenDeployer); !ok {
					t.Errorf("NewDeployer() = %T, want *ConfigDrivenDeployer", got)
				}
			}
		})
	}
}

func TestConfigDrivenDeployer_ReplacePath(t *testing.T) {
	tests := []struct {
		name   string
		domain string
		path   string
		want   string
	}{
		{
			name:   "替换单个占位符",
			domain: "example.com",
			path:   "/certs/{domain}/cert.pem",
			want:   "/certs/example.com/cert.pem",
		},
		{
			name:   "替换多个占位符",
			domain: "test.org",
			path:   "/ssl/{domain}/{domain}.crt",
			want:   "/ssl/test.org/test.org.crt",
		},
		{
			name:   "无占位符不变",
			domain: "example.com",
			path:   "/etc/nginx/ssl/cert.pem",
			want:   "/etc/nginx/ssl/cert.pem",
		},
		{
			name:   "空域名不替换",
			domain: "",
			path:   "/certs/{domain}/cert.pem",
			want:   "/certs/{domain}/cert.pem",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &ConfigDrivenDeployer{
				cfg: DeploymentConfig{Domain: tt.domain},
			}
			got := d.replacePath(tt.path)
			if got != tt.want {
				t.Errorf("replacePath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestConfigDrivenDeployer_Deploy_DryRun(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := DeploymentConfig{
		Domain:        "example.com",
		CertPath:      filepath.Join(tmpDir, "certs", "{domain}", "cert.pem"),
		KeyPath:       filepath.Join(tmpDir, "certs", "{domain}", "key.pem"),
		FullchainPath: filepath.Join(tmpDir, "certs", "{domain}", "fullchain.pem"),
	}

	deployer := &ConfigDrivenDeployer{cfg: cfg}
	certs := &client.CertificateFiles{
		Cert:      []byte("cert content"),
		Key:       []byte("key content"),
		Fullchain: []byte("fullchain content"),
	}

	// DryRun 模式不应写入任何文件
	err := deployer.Deploy(certs, true)
	if err != nil {
		t.Errorf("Deploy() dryRun error = %v", err)
	}

	// 验证本测试临时目录内文件未创建
	paths := []string{
		filepath.Join(tmpDir, "certs", "example.com", "cert.pem"),
		filepath.Join(tmpDir, "certs", "example.com", "key.pem"),
		filepath.Join(tmpDir, "certs", "example.com", "fullchain.pem"),
	}
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			t.Errorf("DryRun 模式不应创建文件: %s", path)
		}
	}
}

// 与 Daemon 共用空内容规则：任一配置目标的源内容为空即拒绝部署
func TestConfigDrivenDeployer_Deploy_EmptyContent(t *testing.T) {
	tests := []struct {
		name  string
		cfg   func(dir string) DeploymentConfig
		certs func() *client.CertificateFiles
	}{
		{
			name:  "空证书内容",
			cfg:   func(dir string) DeploymentConfig { return DeploymentConfig{CertPath: filepath.Join(dir, "cert.pem")} },
			certs: func() *client.CertificateFiles { return &client.CertificateFiles{Cert: []byte{}} },
		},
		{
			name:  "空私钥内容",
			cfg:   func(dir string) DeploymentConfig { return DeploymentConfig{KeyPath: filepath.Join(dir, "key.pem")} },
			certs: func() *client.CertificateFiles { return &client.CertificateFiles{Key: []byte{}} },
		},
		{
			name: "空证书链内容",
			cfg: func(dir string) DeploymentConfig {
				return DeploymentConfig{FullchainPath: filepath.Join(dir, "fullchain.pem")}
			},
			certs: func() *client.CertificateFiles { return &client.CertificateFiles{Fullchain: []byte{}} },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deployer := &ConfigDrivenDeployer{cfg: tt.cfg(t.TempDir())}
			if err := deployer.Deploy(tt.certs(), false); err == nil {
				t.Error("Deploy() 应在内容为空时返回错误")
			}
		})
	}
}

func TestConfigDrivenDeployer_Deploy_WriteFailurePropagates(t *testing.T) {
	// 目标路径是已存在的目录：临时文件重命名替换目录必然失败
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.MkdirAll(blocker, 0755); err != nil {
		t.Fatal(err)
	}

	deployer := &ConfigDrivenDeployer{cfg: DeploymentConfig{
		Domain:   "example.com",
		CertPath: blocker,
	}}
	certs := &client.CertificateFiles{Cert: []byte("cert content")}

	err := deployer.Deploy(certs, false)
	if err == nil {
		t.Fatal("目标不可写时 Deploy() 应返回错误")
	}
	if matches, _ := filepath.Glob(blocker + ".tmp-*"); len(matches) != 0 {
		t.Errorf("写入失败后应清理临时文件，残留: %v", matches)
	}
}

func TestConfigDrivenDeployer_Deploy_FullFlow(t *testing.T) {
	// 使用临时目录进行完整流程测试
	tmpDir := t.TempDir()

	cfg := DeploymentConfig{
		Domain:        "test.example.com",
		CertPath:      filepath.Join(tmpDir, "{domain}", "cert.pem"),
		KeyPath:       filepath.Join(tmpDir, "{domain}", "key.pem"),
		FullchainPath: filepath.Join(tmpDir, "{domain}", "fullchain.pem"),
	}

	// 预置旧文件（权限 0644），部署应原子替换并收紧 key 权限
	domainDir := filepath.Join(tmpDir, "test.example.com")
	if err := os.MkdirAll(domainDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(domainDir, "key.pem"), []byte("stale"), 0644); err != nil {
		t.Fatal(err)
	}

	deployer := NewDeployer(cfg)

	certs := &client.CertificateFiles{
		Cert:      []byte("-----BEGIN CERTIFICATE-----\ntest cert\n-----END CERTIFICATE-----"),
		Key:       []byte("-----BEGIN PRIVATE KEY-----\ntest key\n-----END PRIVATE KEY-----"),
		Fullchain: []byte("-----BEGIN CERTIFICATE-----\ntest fullchain\n-----END CERTIFICATE-----"),
	}

	if err := deployer.Deploy(certs, false); err != nil {
		t.Fatalf("Deploy() error = %v", err)
	}

	// 验证所有文件已创建：内容 + 权限（key.pem 0600，其余 0644）
	expectedFiles := map[string]struct {
		content []byte
		perm    os.FileMode
	}{
		filepath.Join(domainDir, "cert.pem"):      {certs.Cert, 0644},
		filepath.Join(domainDir, "key.pem"):       {certs.Key, 0600},
		filepath.Join(domainDir, "fullchain.pem"): {certs.Fullchain, 0644},
	}

	for path, want := range expectedFiles {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("读取文件 %s 失败: %v", path, err)
			continue
		}
		if string(data) != string(want.content) {
			t.Errorf("文件 %s 内容不匹配", path)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("stat %s 失败: %v", path, err)
			continue
		}
		if info.Mode().Perm() != want.perm {
			t.Errorf("文件 %s 权限 = %o, want %o", path, info.Mode().Perm(), want.perm)
		}
	}
}

func TestConfigDrivenDeployer_Deploy_PartialConfig(t *testing.T) {
	// 测试只配置部分路径的情况
	tmpDir := t.TempDir()

	cfg := DeploymentConfig{
		Domain:   "partial.com",
		CertPath: filepath.Join(tmpDir, "cert.pem"),
		// KeyPath 和 FullchainPath 未配置
	}

	deployer := NewDeployer(cfg)

	certs := &client.CertificateFiles{
		Cert:      []byte("cert only"),
		Key:       []byte("key content"),
		Fullchain: []byte("fullchain content"),
	}

	err := deployer.Deploy(certs, false)
	if err != nil {
		t.Fatalf("Deploy() error = %v", err)
	}

	// 验证只有 cert 被写入
	certPath := filepath.Join(tmpDir, "cert.pem")
	if _, err := os.Stat(certPath); os.IsNotExist(err) {
		t.Error("cert.pem 应该被创建")
	}

	// key 和 fullchain 不应存在
	keyPath := filepath.Join(tmpDir, "key.pem")
	if _, err := os.Stat(keyPath); err == nil {
		t.Error("key.pem 不应存在（未配置）")
	}
}
