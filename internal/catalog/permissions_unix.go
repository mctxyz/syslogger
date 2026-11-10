//go:build unix

package catalog

import (
	"fmt"
	"os"
	"syscall"
)

// checkGroup 以初始化根目录的 GID 校验已打开对象，不修改创建者 UID。
func (s Store) checkGroup(file *os.File) error {
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
	if actual.Gid != owner.Gid {
		return fmt.Errorf("名称目录对象 %s 的属组需要与根目录一致（GID=%d，当前GID=%d），请管理员人工处理: %w", file.Name(), owner.Gid, actual.Gid, os.ErrPermission)
	}
	return nil
}

// openProjectDirectory 拒绝目录入口的符号链接，后续元数据设置绑定到目录 fd。
func openProjectDirectory(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
}
