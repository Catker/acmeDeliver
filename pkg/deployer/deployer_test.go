package deployer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Catker/acmeDeliver/pkg/client"
)

func TestNewDeployer_NoOpDeployer(t *testing.T) {
	// 不配置任何路径时，应返回 NoOpDeployer
	cfg := DeploymentConfig{
		Domain:    "example.com",
		CertPath:  "",
		KeyPath:   "",
		ReloadCmd: "echo reload",
	}

	deployer, err := NewDeployer(cfg)
	if err != nil {
		t.Fatalf("NewDeployer() error = %v", err)
	}

	if _, ok := deployer.(*NoOpDeployer); !ok {
		t.Errorf("NewDeployer() = %T, want *NoOpDeployer", deployer)
	}
}

func TestNewDeployer_ConfigDrivenDeployer(t *testing.T) {
	// 配置了路径时，应返回 ConfigDrivenDeployer
	cfg := DeploymentConfig{
		Domain:   "example.com",
		CertPath: "/tmp/cert.pem",
	}

	deployer, err := NewDeployer(cfg)
	if err != nil {
		t.Fatalf("NewDeployer() error = %v", err)
	}

	if _, ok := deployer.(*ConfigDrivenDeployer); !ok {
		t.Errorf("NewDeployer() = %T, want *ConfigDrivenDeployer", deployer)
	}
}

func TestNoOpDeployer_Deploy(t *testing.T) {
	deployer := &NoOpDeployer{}
	certs := &client.CertificateFiles{
		Cert: []byte("cert content"),
		Key:  []byte("key content"),
	}

	err := deployer.Deploy(certs, false)
	if err != nil {
		t.Errorf("NoOpDeployer.Deploy() error = %v, want nil", err)
	}

	err = deployer.Deploy(certs, true)
	if err != nil {
		t.Errorf("NoOpDeployer.Deploy() dryRun error = %v, want nil", err)
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
	cfg := DeploymentConfig{
		Domain:        "example.com",
		CertPath:      "/tmp/certs/{domain}/cert.pem",
		KeyPath:       "/tmp/certs/{domain}/key.pem",
		FullchainPath: "/tmp/certs/{domain}/fullchain.pem",
		ReloadCmd:     "nginx -s reload",
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

	// 验证文件未创建
	paths := []string{
		"/tmp/certs/example.com/cert.pem",
		"/tmp/certs/example.com/key.pem",
		"/tmp/certs/example.com/fullchain.pem",
	}
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			t.Errorf("DryRun 模式不应创建文件: %s", path)
			os.Remove(path)
		}
	}
}

func TestConfigDrivenDeployer_Deploy_EmptyContent(t *testing.T) {
	cfg := DeploymentConfig{
		Domain:   "example.com",
		CertPath: "/tmp/test-cert.pem",
	}

	deployer := &ConfigDrivenDeployer{cfg: cfg}
	certs := &client.CertificateFiles{
		Cert: []byte{}, // 空内容
	}

	err := deployer.Deploy(certs, false)
	if err == nil {
		t.Error("Deploy() 应在证书内容为空时返回错误")
	}
}

func TestConfigDrivenDeployer_WriteFile(t *testing.T) {
	// 使用临时目录
	tmpDir := t.TempDir()

	tests := []struct {
		name    string
		path    string
		content []byte
		wantErr bool
	}{
		{
			name:    "正常写入",
			path:    filepath.Join(tmpDir, "test.pem"),
			content: []byte("test content"),
			wantErr: false,
		},
		{
			name:    "创建子目录并写入",
			path:    filepath.Join(tmpDir, "subdir", "nested", "cert.pem"),
			content: []byte("nested content"),
			wantErr: false,
		},
		{
			name:    "空路径",
			path:    "",
			content: []byte("content"),
			wantErr: true,
		},
		{
			name:    "空内容",
			path:    filepath.Join(tmpDir, "empty.pem"),
			content: []byte{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &ConfigDrivenDeployer{}
			err := d.writeFile(tt.path, tt.content)

			if (err != nil) != tt.wantErr {
				t.Errorf("writeFile() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && tt.path != "" {
				// 验证文件已写入且内容正确
				data, err := os.ReadFile(tt.path)
				if err != nil {
					t.Errorf("读取写入的文件失败: %v", err)
					return
				}
				if string(data) != string(tt.content) {
					t.Errorf("文件内容 = %q, want %q", string(data), string(tt.content))
				}
			}
		})
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
		SkipReload:    true, // 跳过 reload 命令
	}

	deployer, err := NewDeployer(cfg)
	if err != nil {
		t.Fatalf("NewDeployer() error = %v", err)
	}

	certs := &client.CertificateFiles{
		Cert:      []byte("-----BEGIN CERTIFICATE-----\ntest cert\n-----END CERTIFICATE-----"),
		Key:       []byte("-----BEGIN PRIVATE KEY-----\ntest key\n-----END PRIVATE KEY-----"),
		Fullchain: []byte("-----BEGIN CERTIFICATE-----\ntest fullchain\n-----END CERTIFICATE-----"),
	}

	err = deployer.Deploy(certs, false)
	if err != nil {
		t.Fatalf("Deploy() error = %v", err)
	}

	// 验证所有文件已创建
	expectedFiles := map[string][]byte{
		filepath.Join(tmpDir, "test.example.com", "cert.pem"):      certs.Cert,
		filepath.Join(tmpDir, "test.example.com", "key.pem"):       certs.Key,
		filepath.Join(tmpDir, "test.example.com", "fullchain.pem"): certs.Fullchain,
	}

	for path, expectedContent := range expectedFiles {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("读取文件 %s 失败: %v", path, err)
			continue
		}
		if string(data) != string(expectedContent) {
			t.Errorf("文件 %s 内容不匹配", path)
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
		SkipReload: true,
	}

	deployer, err := NewDeployer(cfg)
	if err != nil {
		t.Fatalf("NewDeployer() error = %v", err)
	}

	certs := &client.CertificateFiles{
		Cert:      []byte("cert only"),
		Key:       []byte("key content"),
		Fullchain: []byte("fullchain content"),
	}

	err = deployer.Deploy(certs, false)
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
