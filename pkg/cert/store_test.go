package cert

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// newTestStore 在临时目录下按 domain -> 文件内容 创建证书目录并返回 Store
func newTestStore(t *testing.T, domains map[string]map[string]string) *Store {
	t.Helper()
	baseDir := t.TempDir()
	for domain, files := range domains {
		dir := filepath.Join(baseDir, domain)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		for name, content := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
	// baseDir 下的普通文件不应被当作域名
	if err := os.WriteFile(filepath.Join(baseDir, "a.example.com.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	return NewStore(baseDir)
}

func TestStore_Load(t *testing.T) {
	s := newTestStore(t, map[string]map[string]string{
		"example.com": {"cert.pem": "cert", "time.log": "1700000000", "readme.txt": "ignored"},
		"empty.com":   {"readme.txt": "ignored"},
	})

	tests := []struct {
		name      string
		domain    string
		wantErr   error
		wantFiles []string
	}{
		{"正常读取下发文件", "example.com", nil, []string{"cert.pem", "time.log"}},
		{"空域名非法", "", ErrInvalidDomain, nil},
		{"路径穿越非法", "../etc", ErrInvalidDomain, nil},
		{"含斜杠非法", "a/b", ErrInvalidDomain, nil},
		{"目录不存在", "missing.com", ErrDomainNotFound, nil},
		{"没有下发文件", "empty.com", ErrNoFiles, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files, err := s.Load(tt.domain)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Load(%q) error = %v, want %v", tt.domain, err, tt.wantErr)
			}
			if len(files) != len(tt.wantFiles) {
				t.Fatalf("Load(%q) 返回 %d 个文件，期望 %v", tt.domain, len(files), tt.wantFiles)
			}
			for _, name := range tt.wantFiles {
				if _, ok := files[name]; !ok {
					t.Errorf("Load(%q) 缺少文件 %s", tt.domain, name)
				}
			}
		})
	}
}

func TestStore_Timestamp(t *testing.T) {
	s := newTestStore(t, map[string]map[string]string{
		"example.com": {"time.log": "1700000000\n"},
		"notime.com":  {"cert.pem": "cert"},
		"bad.com":     {"time.log": "not-a-number"},
	})

	tests := []struct {
		name   string
		domain string
		want   int64
	}{
		{"正常解析", "example.com", 1700000000},
		{"time.log 缺失为 0", "notime.com", 0},
		{"time.log 非法为 0", "bad.com", 0},
		{"目录不存在为 0", "missing.com", 0},
		{"域名非法为 0", "../etc", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := s.Timestamp(tt.domain); got != tt.want {
				t.Errorf("Timestamp(%q) = %d, want %d", tt.domain, got, tt.want)
			}
		})
	}
}

func TestStore_Match(t *testing.T) {
	s := newTestStore(t, map[string]map[string]string{
		"a.example.com": {},
		"b.example.com": {},
		"*.example.com": {},
		"example.com":   {},
		"other.org":     {},
	})

	tests := []struct {
		name    string
		pattern string
		want    []string
	}{
		{"星号匹配全部目录", "*", []string{"*.example.com", "a.example.com", "b.example.com", "example.com", "other.org"}},
		{"通配匹配子域名与同名目录", "*.example.com", []string{"*.example.com", "a.example.com", "b.example.com"}},
		{"通配无匹配", "*.none.com", nil},
		{"精确域名原样返回", "a.example.com", []string{"a.example.com"}},
		{"精确域名不检查目录存在", "missing.com", []string{"missing.com"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.Match(tt.pattern)
			if err != nil {
				t.Fatalf("Match(%q) error = %v", tt.pattern, err)
			}
			slices.Sort(got)
			if !slices.Equal(got, tt.want) {
				t.Errorf("Match(%q) = %v, want %v", tt.pattern, got, tt.want)
			}
		})
	}
}

func TestStore_Match_BaseDirMissing(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "missing"))
	if _, err := s.Match("*"); err == nil {
		t.Error("Match(\"*\") 证书目录不存在时应返回错误")
	}
}
