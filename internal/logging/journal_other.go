//go:build !linux

package logging

import (
	"net"
	"syscall"
)

// 非 Linux 平台通过显式 SyslogSocketPath 使用传统协议，原生 journal 写入返回不支持。
func writeJournal(conn *net.UnixConn, data []byte) error {
	return syscall.EPROTONOSUPPORT
}
