//go:build !linux

package hostinit

import (
	"context"
	"fmt"
	"io"
)

// Run 的主机配置能力依赖 Linux 和 systemd，其他平台保留查询入口。
func Run(_ context.Context, _ Options, _ io.Writer) error {
	return fmt.Errorf("mct-syslogger init 需要 Linux 和 systemd 245+")
}
