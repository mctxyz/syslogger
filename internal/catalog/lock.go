package catalog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/gofrs/flock"
)

// lockProject 使用稳定的项目锁文件协调合并和清理，实例 JSON 的原子替换不会更换锁 inode。
// 锁文件与共享清单采用相同的跨用户维护权限，保持到项目目录生命周期结束。
func lockProject(directory string) (*flock.Flock, error) {
	if err := sharedDirectory(directory); err != nil {
		return nil, err
	}
	path := filepath.Join(directory, LockName)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, FileMode)
	created := err == nil
	if errors.Is(err, os.ErrExist) {
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return nil, statErr
		}
		if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0002 != 0) {
			return nil, fmt.Errorf("项目锁 %s 需要通过 mct-syslogger init 修复权限", path)
		}
		file, err = os.OpenFile(path, os.O_RDWR, 0)
	}
	if err != nil {
		return nil, err
	}
	if created {
		err = file.Chmod(FileMode)
	}
	closeErr := file.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	lock := flock.New(path, flock.SetFlag(os.O_RDWR))
	if err := lock.Lock(); err != nil {
		return nil, err
	}
	return lock, nil
}
