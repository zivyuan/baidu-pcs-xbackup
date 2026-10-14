package baidupan

// Provider 网盘操作抽象,两种实现:
//   - Client(oauth):开放平台 OAuth 授权,需企业 AppKey
//   - WebClient(web):网页版接口 + BDUSS Cookie,个人账号可用
//
// 消费方(backup/retention/cmd)只依赖此接口。
type Provider interface {
	// EnsureToken 加载/校验凭证;失效时返回需重新 login 的错误。
	EnsureToken() error
	MkdirAll(dir string) error
	List(dir string) ([]Entry, error)
	// Download 按路径取回小文件内容(fingerprint 比对用)。
	Download(path string) ([]byte, error)
	Delete(paths []string) error
	UploadFile(remotePath, localPath string) error
}
