package client

// CertificateFiles 证书文件结构
type CertificateFiles struct {
	Cert      []byte `json:"cert"`
	Key       []byte `json:"key"`
	Fullchain []byte `json:"fullchain"`
	TimeLog   []byte `json:"time_log"`  // 服务端 time.log 原始内容
	Timestamp int64  `json:"timestamp"` // 服务端证书时间戳（0 表示未知）
}

// IsEmpty 检查证书文件是否为空
func (c *CertificateFiles) IsEmpty() bool {
	return len(c.Cert) == 0 && len(c.Key) == 0 && len(c.Fullchain) == 0
}
