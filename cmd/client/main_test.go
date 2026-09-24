package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

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
	require.Equal(t, "/var/lib/acmedeliver", cfg.WorkDir)
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

func TestExecuteReloadCommandsReturnsErrorOnFailure(t *testing.T) {
	require.Error(t, executeReloadCommands(map[string]bool{"true": true, "false": true}, false))
	require.NoError(t, executeReloadCommands(map[string]bool{"true": true}, false))
	// dry-run 不执行命令，不返回错误
	require.NoError(t, executeReloadCommands(map[string]bool{"false": true}, true))
}

func TestExpiryStatus(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	day := 24 * time.Hour
	tests := []struct {
		name         string
		offset       time.Duration
		expired      bool
		expiringSoon bool
		icon         string
		text         string
	}{
		{"剩余 90 天", 90*day + time.Hour, false, false, "🟢", "剩余 90 天"},
		{"剩余 30 天", 30*day + time.Hour, false, false, "🟡", "剩余 30 天"},
		{"剩余 7 天", 7*day + time.Hour, false, true, "🔴", "剩余 7 天"},
		{"剩余 12 小时未过期", 12 * time.Hour, false, true, "🔴", "剩余不足 1 天"},
		{"恰好到期", 0, true, false, "🔴", "已过期不足 1 天"},
		{"过期 12 小时", -12 * time.Hour, true, false, "🔴", "已过期不足 1 天"},
		{"过期 3 天", -3*day - time.Hour, true, false, "🔴", "已过期 3 天"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expired, soon, icon, text := expiryStatus(now.Add(tt.offset).Unix(), now)
			require.Equal(t, tt.expired, expired)
			require.Equal(t, tt.expiringSoon, soon)
			require.Equal(t, tt.icon, icon)
			require.Equal(t, tt.text, text)
		})
	}
}
