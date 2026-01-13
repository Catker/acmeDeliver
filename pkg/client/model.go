package client

// CertificateFiles 证书文件结构
type CertificateFiles struct {
	Cert      []byte `json:"cert"`
	Key       []byte `json:"key"`
	Fullchain []byte `json:"fullchain"`
}

// IsEmpty 检查证书文件是否为空
func (c *CertificateFiles) IsEmpty() bool {
	return len(c.Cert) == 0 && len(c.Key) == 0 && len(c.Fullchain) == 0
}

// FileCount 返回非空文件的数量
func (c *CertificateFiles) FileCount() int {
	count := 0
	if len(c.Cert) > 0 {
		count++
	}
	if len(c.Key) > 0 {
		count++
	}
	if len(c.Fullchain) > 0 {
		count++
	}
	return count
}

// TotalSize 返回所有文件的总大小
func (c *CertificateFiles) TotalSize() int {
	return len(c.Cert) + len(c.Key) + len(c.Fullchain)
}
