package hostinit

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mctxyz/syslogger/internal/catalog"
	"golang.org/x/sys/unix"
)

// openDirectory 拒绝目录入口的符号链接，后续迁移通过目录 fd 定位子对象。
func openDirectory(path string, create bool, mode os.FileMode) (*os.File, error) {
	if create {
		if err := os.Mkdir(path, mode); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("打开目录 %s: %w", path, err)
	}
	return os.NewFile(uintptr(fd), path), nil
}

// inspect 拒绝特殊文件、多重硬链接及 ACL，防止特权迁移影响受管目录外对象。
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

func prepareCatalog(path string, gid int) error {
	root, err := openDirectory(path, true, catalog.DirectoryMode)
	if err != nil {
		return err
	}
	defer root.Close()
	return migrateDirectory(root, gid, true)
}

// migrateDirectory 保留文件内容及锁 inode，先收紧目录再按 fd 迁移受管子对象。
func migrateDirectory(directory *os.File, gid int, root bool) error {
	if _, err := inspect(directory, true); err != nil {
		return err
	}
	if err := directory.Chown(os.Geteuid(), gid); err != nil {
		return err
	}
	if err := directory.Chmod(catalog.DirectoryMode); err != nil {
		return err
	}
	names, err := directory.Readdirnames(-1)
	if err != nil {
		return err
	}
	for _, name := range names {
		if root && !digestName(name) {
			return fmt.Errorf("名称根目录存在非项目对象 %s，请管理员先处理", filepath.Join(directory.Name(), name))
		}
		instanceFile := strings.HasSuffix(name, ".json") && digestName(strings.TrimSuffix(name, ".json"))
		temporaryFile := strings.HasPrefix(name, ".catalog-")
		if !root && name != catalog.LockName && !temporaryFile && !instanceFile {
			return fmt.Errorf("项目目录存在非受管对象 %s，请管理员先处理", filepath.Join(directory.Name(), name))
		}
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
		if root {
			flags |= unix.O_DIRECTORY
		}
		fd, err := unix.Openat(int(directory.Fd()), name, flags, 0)
		if err != nil {
			return fmt.Errorf("打开受管对象 %s: %w", filepath.Join(directory.Name(), name), err)
		}
		file := os.NewFile(uintptr(fd), filepath.Join(directory.Name(), name))
		if root {
			err = migrateDirectory(file, gid, false)
		} else {
			_, err = inspect(file, false)
			if err == nil {
				err = file.Chown(os.Geteuid(), gid)
			}
			if err == nil {
				err = file.Chmod(catalog.FileMode)
			}
		}
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func digestName(name string) bool {
	if len(name) != 64 {
		return false
	}
	_, err := hex.DecodeString(name)
	return err == nil
}
