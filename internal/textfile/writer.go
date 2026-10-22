package textfile

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
)

// Write 在同目录完整准备临时文件，再原子替换正式文件；调用方串行协调写入。
// 所有实际文件错误使用进程默认日志器反馈，本次失败仅清理本次临时资源。
func Write(dir, filename string, data []byte) (err error) {
	file, err := os.CreateTemp(dir, "."+filename+".tmp-*")
	if err != nil {
		return fileError("创建临时文件", err)
	}
	temporary := file.Name()
	renamed := false
	defer func() {
		if file != nil {
			if closeErr := file.Close(); closeErr != nil {
				closeErr = fileError("关闭临时文件", closeErr)
				if err == nil {
					err = closeErr
				}
			}
		}
		if !renamed {
			if removeErr := os.Remove(temporary); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				removeErr = fileError("清理临时文件", removeErr)
				if err == nil {
					err = removeErr
				}
			}
		}
	}()
	if written, writeErr := file.Write(data); writeErr != nil {
		return fileError("写入临时文件", writeErr)
	} else if written != len(data) {
		return fileError("写入临时文件", io.ErrShortWrite)
	}
	if err := file.Chmod(0644); err != nil {
		return fileError("设置文件权限", err)
	}
	closeErr := file.Close()
	file = nil
	if closeErr != nil {
		return fileError("关闭临时文件", closeErr)
	}
	if err := os.Rename(temporary, filepath.Join(dir, filename)); err != nil {
		return fileError("原子替换正式文件", err)
	}
	renamed = true
	return nil
}

func fileError(operation string, err error) error {
	log.Printf("syslogger: %s: %v", operation, err)
	return fmt.Errorf("syslogger: %s: %w", operation, err)
}
