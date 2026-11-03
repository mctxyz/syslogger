package catalog

import (
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
)

// lockProject 使用稳定的项目锁文件协调合并和清理，实例 JSON 的原子替换不会更换锁 inode。
// 锁文件与共享清单采用相同的跨用户维护权限，保持到项目目录生命周期结束。
func lockProject(directory string) (*flock.Flock, error) {
	if err := sharedDirectory(directory); err != nil {
		return nil, err
	}
	path := filepath.Join(directory, ".catalog.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0666)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err == nil && info.Mode().Perm() != 0666 {
		err = file.Chmod(0666)
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
