//go:build !linux

package logging

import (
	"net"
	"syscall"
)

// 非 Linux 平台继续使用 syslog；保留该边界使共享发送流程可跨平台编译。
func writeJournal(conn *net.UnixConn, data []byte) error {
	return syscall.EPROTONOSUPPORT
}
