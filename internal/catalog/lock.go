package catalog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/gofrs/flock"
)

// lockProject 使用组内共享的稳定锁协调合并和清理，实例 JSON 替换不会更换锁 inode。
func (s Store) lockProject(directory string) (*flock.Flock, error) {
	if err := s.projectDirectory(directory); err != nil {
		return nil, err
	}
	if err := s.prepareLock(directory); err != nil {
		return nil, err
	}
	path := filepath.Join(directory, LockName)
	lock := flock.New(path, flock.SetFlag(os.O_RDWR))
	if err := lock.Lock(); err != nil {
		return nil, err
	}
	return lock, nil
}

// prepareLock 先完成权限设置再以 Link 发布 inode，跨 UID 首次加锁无需等待权限修复。
func (s Store) prepareLock(directory string) error {
	path := filepath.Join(directory, LockName)
	err := s.checkLock(path)
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.CreateTemp(directory, ".catalog-lock-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := s.checkGroup(file); err != nil {
		return err
	}
	if err := file.Chmod(FileMode); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Link(file.Name(), path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return s.checkLock(path)
		}
		return err
	}
	return nil
}

func (s Store) checkLock(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm() != FileMode) {
		return fmt.Errorf("项目锁 %s 的类型或权限不安全，需要 0664 权限，请管理员人工处理: %w", path, os.ErrPermission)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	err = s.checkGroup(file)
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}
