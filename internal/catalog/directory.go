package catalog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// projectTemporaryPrefix 标识尚未原子发布的项目，读取和清理均跳过它们。
const projectTemporaryPrefix = ".catalog-project-"

// CheckRoot 复用管理员初始化的根目录，SDK 不创建或修复这个授权入口。
func (s Store) CheckRoot() error {
	err := checkDirectory(s.Root)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("名称根目录缺失，请运行 mct-syslogger init 创建: %w", err)
	}
	return err
}

func checkDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("名称目录 %s 不存在: %w", path, err)
	}
	if err != nil {
		return fmt.Errorf("名称目录 %s 不可用，请管理员人工处理: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("名称目录 %s 不是普通目录，请管理员人工处理", path)
	}
	if runtime.GOOS != "windows" && info.Mode()&(os.ModePerm|os.ModeSetgid|os.ModeSticky) != DirectoryMode {
		return fmt.Errorf("名称目录 %s 需要 2775 权限，请管理员人工处理: %w", path, os.ErrPermission)
	}
	return nil
}

func (s Store) checkProjectDirectory(path string) error {
	if err := checkDirectory(path); err != nil {
		return err
	}
	directory, err := openProjectDirectory(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return s.checkGroup(directory)
}

// projectDirectory 准备好组权限及稳定锁后才发布项目，其他 UID 不会看到半成品。
func (s Store) projectDirectory(path string) error {
	err := s.checkProjectDirectory(path)
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary, err := os.MkdirTemp(s.Root, projectTemporaryPrefix)
	if err != nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			os.Remove(filepath.Join(temporary, LockName))
			os.Remove(temporary)
		}
	}()
	if err := s.prepareLock(temporary); err != nil {
		return err
	}
	directory, err := openProjectDirectory(temporary)
	if err != nil {
		return err
	}
	err = s.checkGroup(directory)
	if err == nil {
		err = directory.Chmod(DirectoryMode)
	}
	closeErr := directory.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(temporary, path); err != nil {
		// 每个配套 SDK 发布的项目都有稳定锁，非空目录不会被并发创建者替换。
		if errors.Is(err, os.ErrExist) {
			return s.checkProjectDirectory(path)
		}
		return err
	}
	published = true
	return nil
}
