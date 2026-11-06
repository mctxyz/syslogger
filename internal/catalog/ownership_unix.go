//go:build unix

package catalog

import (
	"os"
	"syscall"
)

// inheritOwner 以 init 准备的根目录为身份来源，让 root 和 app 创建相同属主的对象。
// 仅修改已打开的新对象，避免通过路径 chown 跟随被替换的符号链接。
func (s Store) inheritOwner(file *os.File) error {
	root, err := os.Lstat(s.Root)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	owner := root.Sys().(*syscall.Stat_t)
	actual := info.Sys().(*syscall.Stat_t)
	if actual.Uid == owner.Uid && actual.Gid == owner.Gid {
		return nil
	}
	return file.Chown(int(owner.Uid), int(owner.Gid))
}

// openProjectDirectory 拒绝目录入口的符号链接，后续元数据设置绑定到目录 fd。
func openProjectDirectory(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
}
