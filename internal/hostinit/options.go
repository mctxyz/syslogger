// Package hostinit 准备本机名称目录权限及专用 journal 实例。
package hostinit

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/mctxyz/syslogger/internal/journalwire"
)

// DefaultJournalMaxUse 为 info 分配 2 GiB，其余三组各 1 GiB，默认合计 5 GiB。
func DefaultJournalMaxUse(group journalwire.Group) string {
	if group == journalwire.InfoGroup {
		return "2G"
	}
	return "1G"
}

// Options 由 init 子命令提供四个等级空间的容量。
type Options struct {
	// JournalMaxUse 按 journalwire.Group 索引保存各空间的独立容量。
	JournalMaxUse [journalwire.GroupCount]string
}

// Validate 在特权操作前检查容量，拒绝配置文件注入。
func (o Options) Validate() error {
	for group := journalwire.DebugGroup; group < journalwire.GroupCount; group++ {
		if _, err := sizeBytes(o.JournalMaxUse[group]); err != nil {
			return fmt.Errorf("--journal-%s-max-use: %w", group.Name(), err)
		}
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
