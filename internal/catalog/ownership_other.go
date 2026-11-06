//go:build !unix

package catalog

import "os"

// inheritOwner 在非 Unix 平台沿用本地文件系统身份；固定 app 的主机初始化由 Linux 提供。
func (s Store) inheritOwner(file *os.File) error {
	return nil
}

// openProjectDirectory 在本地文件系统上打开已经创建的目录。
func openProjectDirectory(path string) (*os.File, error) {
	return os.Open(path)
}
