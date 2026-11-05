// Package hostinit 准备本机名称目录权限及专用 journal 实例。
package hostinit

import (
	"fmt"
	"strconv"
	"strings"
)

// DefaultJournalMaxUse 是专用命名空间的默认持久化容量。
const DefaultJournalMaxUse = "5G"

// Options 仅由 init 子命令提供，运行账户必须明确指定。
type Options struct {
	User          string
	JournalMaxUse string
}

// Validate 在特权操作前检查用户参数和容量，拒绝配置文件注入。
func (o Options) Validate() error {
	if o.User == "" || strings.ContainsAny(o.User, "\x00\r\n:/") {
		return fmt.Errorf("--user 必须指定已存在的程序运行账户")
	}
	if _, err := sizeBytes(o.JournalMaxUse); err != nil {
		return fmt.Errorf("--journal-max-use: %w", err)
	}
	return nil
}

// sizeBytes 接受正整数字节或 systemd 的 1024 进制 K/M/G/T/P/E 单位。
func sizeBytes(value string) (uint64, error) {
	text := value
	var shift uint
	if len(text) != 0 {
		if index := strings.IndexByte("KMGTPE", text[len(text)-1]); index >= 0 {
			shift = uint(index+1) * 10
			text = text[:len(text)-1]
		}
	}
	amount, err := strconv.ParseUint(text, 10, 64)
	if err != nil || amount == 0 || amount > ^uint64(0)>>shift {
		return 0, fmt.Errorf("容量 %q 无效，请使用正整数及可选的 K/M/G/T/P/E 单位，例如 5G", value)
	}
	return amount << shift, nil
}
