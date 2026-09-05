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
