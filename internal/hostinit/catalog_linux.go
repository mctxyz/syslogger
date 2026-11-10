package hostinit

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mctxyz/syslogger/internal/catalog"
	"golang.org/x/sys/unix"
)

// prepareCatalog 原地修正根目录和已有受管对象；迁移前由部署方停止相关写入进程。
func (init *initializer) prepareCatalog(path string, uid, gid int) error {
	root, _, err := init.openDirectory(path, catalog.DirectoryMode)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := init.repairCatalogObject(root, true, uid, gid); err != nil {
		return err
	}
	projects, err := root.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range projects {
		if !catalogHashName(entry.Name()) {
			continue
		}
		project, err := openCatalogObject(root, entry.Name(), true)
		if err != nil {
			return err
		}
		err = init.repairCatalogProject(project, uid, gid)
		closeErr := project.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func (init *initializer) repairCatalogProject(project *os.File, uid, gid int) error {
	if err := init.repairCatalogObject(project, true, uid, gid); err != nil {
		return err
	}
	entries, err := project.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name != catalog.LockName && !(strings.HasSuffix(name, ".json") && catalogHashName(strings.TrimSuffix(name, ".json"))) {
			continue
		}
		file, err := openCatalogObject(project, name, false)
		if err != nil {
			return err
		}
		err = init.repairCatalogObject(file, false, uid, gid)
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

// catalogHashName 只识别现有 SHA-256 小写路径契约，临时项及其他名称保持原样。
func catalogHashName(name string) bool {
	if len(name) != sha256.Size*2 || name != strings.ToLower(name) {
		return false
	}
	_, err := hex.DecodeString(name)
	return err == nil
}

// openCatalogObject 先检查类型再打开，所有解析相对父 fd，拒绝链接和特殊文件。
func openCatalogObject(parent *os.File, name string, directory bool) (*os.File, error) {
	path := filepath.Join(parent.Name(), name)
	var stat unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, fmt.Errorf("检查名称目录对象 %s: %w", path, err)
	}
	want := uint32(unix.S_IFREG)
	flags := unix.O_RDONLY | unix.O_NONBLOCK | unix.O_NOFOLLOW | unix.O_CLOEXEC
	if directory {
		want = unix.S_IFDIR
		flags |= unix.O_DIRECTORY
	}
	if stat.Mode&unix.S_IFMT != want {
		return nil, fmt.Errorf("名称目录对象 %s 类型不安全，请管理员先处理", path)
	}
	fd, err := unix.Openat(int(parent.Fd()), name, flags, 0)
	if err != nil {
		return nil, fmt.Errorf("打开名称目录对象 %s: %w", path, err)
	}
	return os.NewFile(uintptr(fd), path), nil
}

// repairCatalogObject 仅通过 fd 修改元数据；文件保留 UID、内容及 inode，目录使用 root UID。
func (init *initializer) repairCatalogObject(file *os.File, directory bool, uid, gid int) error {
	stat, err := inspect(file, directory)
	if err != nil {
		return err
	}
	mode := os.FileMode(catalog.FileMode)
	owner := -1
	kind := "文件"
	if directory {
		mode = catalog.DirectoryMode
		owner = uid
		kind = "目录"
	}
	// Go 的 setgid 标志与 Unix 原始位不同，比较和输出使用转换后的权限。
	rawMode := uint32(mode.Perm())
	if mode&os.ModeSetgid != 0 {
		rawMode |= unix.S_ISGID
	}
	ownerChanged := stat.Gid != uint32(gid) || (directory && stat.Uid != uint32(uid))
	if ownerChanged {
		if err := file.Chown(owner, gid); err != nil {
			return err
		}
		if directory {
			init.logger.Printf("[设置目录属主] %q UID=%d GID=%d", file.Name(), uid, gid)
		} else {
			init.logger.Printf("[设置文件属组] %q GID=%d（UID=%d 保持原样）", file.Name(), gid, stat.Uid)
		}
	}
	// chown 可能影响特殊权限位，即使原模式正确也在其后恢复最终模式。
	if ownerChanged || stat.Mode&07777 != rawMode {
		if err := file.Chmod(mode); err != nil {
			return err
		}
		init.logger.Printf("[设置%s权限] %q mode=%04o", kind, file.Name(), rawMode)
	}
	return nil
}
