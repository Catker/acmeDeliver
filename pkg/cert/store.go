package cert

import (
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// Load 的哨兵错误，调用方据此区分失败原因
var (
	ErrInvalidDomain  = errors.New("域名非法")
	ErrDomainNotFound = errors.New("域名不存在")
	ErrNoFiles        = errors.New("没有可用的证书文件")
)

// Store 服务端证书目录（<baseDir>/<domain>/{DeliverFiles}）的只读访问
// 实时推送、CLI 拉取、Daemon 同步补推共用
type Store struct {
	baseDir string
}

// NewStore 创建以 baseDir 为根的证书目录 Store
func NewStore(baseDir string) *Store {
	return &Store{baseDir: baseDir}
}

// Load 校验域名并读取其 DeliverFiles。
// 域名非法返回 ErrInvalidDomain，目录不存在返回 ErrDomainNotFound，
// 一个下发文件都没有返回 ErrNoFiles，其他目录访问错误原样返回
func (s *Store) Load(domain string) (map[string][]byte, error) {
	domainDir, err := SafeDomainDir(s.baseDir, domain)
	if err != nil {
		return nil, ErrInvalidDomain
	}
	if _, err := os.Stat(domainDir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrDomainNotFound
		}
		return nil, err
	}

	files := ReadDeliverFiles(domainDir)
	if len(files) == 0 {
		return nil, ErrNoFiles
	}
	return files, nil
}

// Timestamp 读取域名 time.log 的时间戳；域名非法、文件缺失或内容非法时返回 0
func (s *Store) Timestamp(domain string) int64 {
	domainDir, err := SafeDomainDir(s.baseDir, domain)
	if err != nil {
		slog.Warn("非法域名，跳过时间戳读取", "domain", domain)
		return 0
	}
	content, err := os.ReadFile(filepath.Join(domainDir, "time.log"))
	if err != nil {
		return 0
	}
	return ParseTimeLog(content)
}

// Match 返回匹配订阅 pattern 的域名：
// "*" 匹配 baseDir 下全部域名目录；"*.example.com" 匹配子域名目录，以及字面同名目录（acme.sh 通配证书目录名）；
// 其他 pattern 视为精确域名原样返回（不检查目录是否存在）
func (s *Store) Match(pattern string) ([]string, error) {
	if pattern != "*" && !strings.HasPrefix(pattern, "*.") {
		return []string{pattern}, nil
	}

	entries, err := os.ReadDir(s.baseDir)
	if err != nil {
		return nil, err
	}

	var domains []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		domain := entry.Name()
		if pattern != "*" && domain != pattern && !MatchWildcard(pattern, domain) {
			continue
		}
		domains = append(domains, domain)
	}
	return domains, nil
}
