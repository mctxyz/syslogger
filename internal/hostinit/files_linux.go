package hostinit

import (
	"errors"
	"fmt"
	"os"

	"github.com/mctxyz/syslogger/internal/catalog"
	"golang.org/x/sys/unix"
)

// openDirectory 拒绝目录入口的符号链接，并返回本次是否创建了目录。
func (init *initializer) openDirectory(path string, mode os.FileMode) (*os.File, bool, error) {
	err := os.Mkdir(path, mode)
	if err != nil && !errors.Is(err, os.ErrExist) {
		return nil, false, err
	}
	created := err == nil
	if created {
		init.logger.Printf("[创建目录] %q", path)
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, created, fmt.Errorf("打开目录 %s: %w", path, err)
	}
	if !created {
		init.logger.Printf("[复用目录] %q", path)
	}
	return os.NewFile(uintptr(fd), path), created, nil
}

// inspect 拒绝特殊文件、多重硬链接及 ACL，保护初始化管理的目录和配置文件。
func inspect(file *os.File, directory bool) (unix.Stat_t, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return stat, err
	}
	want := uint32(unix.S_IFREG)
	if directory {
		want = unix.S_IFDIR
	}
	if stat.Mode&unix.S_IFMT != want || (!directory && stat.Nlink != 1) {
		return stat, fmt.Errorf("对象 %s 类型或硬链接数量不安全，请管理员先处理", file.Name())
	}
	for _, name := range []string{"system.posix_acl_access", "system.posix_acl_default"} {
		size, err := unix.Fgetxattr(int(file.Fd()), name, nil)
		if errors.Is(err, unix.ENODATA) || errors.Is(err, unix.ENOTSUP) {
			continue
		}
		if err != nil {
			return stat, fmt.Errorf("检查 %s 的 ACL: %w", file.Name(), err)
		}
		if size > 0 {
			return stat, fmt.Errorf("对象 %s 存在 %s，请管理员先清理 ACL 后重试", file.Name(), name)
		}
	}
	return stat, nil
}

func (init *initializer) prepareCatalog(path string, uid, gid int) error {
	root, created, err := init.openDirectory(path, catalog.DirectoryMode)
	if err != nil {
		return err
	}
	defer root.Close()
	stat, err := inspect(root, true)
	if err != nil {
		return err
	}
	if !created {
		if stat.Uid != uint32(uid) || stat.Gid != uint32(gid) || stat.Mode&07777 != uint32(catalog.DirectoryMode.Perm()) {
			return fmt.Errorf("名称根目录 %s 需要 UID=%d GID=%d 和 %04o 权限，当前 UID=%d GID=%d 权限=%04o，请管理员人工处理后重试", path, uid, gid, catalog.DirectoryMode.Perm(), stat.Uid, stat.Gid, stat.Mode&07777)
		}
		return nil
	}
	if err := root.Chown(uid, gid); err != nil {
		return err
	}
	init.logger.Printf("[设置目录属主] %q UID=%d GID=%d", path, uid, gid)
	if err := root.Chmod(catalog.DirectoryMode); err != nil {
		return err
	}
	init.logger.Printf("[设置目录权限] %q mode=%04o", path, catalog.DirectoryMode.Perm())
	return nil
}
