package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))
	return path
}

func TestLoadConfigurationAllowsCLIOnlyPassword(t *testing.T) {
	oldConfigFile := configFile
	configFile = ""
	t.Cleanup(func() { configFile = oldConfigFile })

	cfg, err := loadConfiguration(&CliOptions{
		Server:   "http://cli-server:9090",
		Password: "cli-password",
	})
	require.NoError(t, err)
	require.Equal(t, "http://cli-server:9090", cfg.Server)
	require.Equal(t, "cli-password", cfg.Password)
	require.Equal(t, "/tmp/acme", cfg.WorkDir)
}

func TestLoadConfigurationRejectsBrokenConfigFile(t *testing.T) {
	oldConfigFile := configFile
	configFile = writeTempConfig(t, "client:\n  password: [broken")
	t.Cleanup(func() { configFile = oldConfigFile })

	_, err := loadConfiguration(&CliOptions{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "加载配置源失败")
}

func TestLoadConfigurationAllowsCLIOverrideMissingPassword(t *testing.T) {
	oldConfigFile := configFile
	configFile = writeTempConfig(t, `
client:
  server: "http://file-config:1111"
  workdir: "/tmp/file-workdir"
`)
	t.Cleanup(func() { configFile = oldConfigFile })

	cfg, err := loadConfiguration(&CliOptions{
		Password: "cli-password",
	})
	require.NoError(t, err)
	require.Equal(t, "http://file-config:1111", cfg.Server)
	require.Equal(t, "cli-password", cfg.Password)
	require.Equal(t, "/tmp/file-workdir", cfg.WorkDir)
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
			require.Equal(t, tt.want, isCertUpToDate(tt.localTS, tt.serverTS))
		})
	}
}
