package logging

import (
	"errors"
	"io"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// writeJournal 先发送数据报，大消息通过密封的匿名内存文件交付，沿用连接写入截止和关闭中断。
func writeJournal(conn *net.UnixConn, data []byte) error {
	n, err := conn.Write(data)
	if err == nil {
		if n != len(data) {
			return io.ErrShortWrite
		}
		return nil
	}
	if !errors.Is(err, unix.EMSGSIZE) && !errors.Is(err, unix.ENOBUFS) {
		return err
	}
	fd, err := unix.MemfdCreate("syslogger-journal", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), "syslogger-journal")
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if _, err := unix.FcntlInt(file.Fd(), unix.F_ADD_SEALS, unix.F_SEAL_SEAL|unix.F_SEAL_SHRINK|unix.F_SEAL_GROW|unix.F_SEAL_WRITE); err != nil {
		return err
	}
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	// Go 的 WriteMsgUnix 拒绝已连接的数据报 socket；RawConn.Write 保留同一连接的 deadline 与关闭唤醒。
	rights := unix.UnixRights(fd)
	var sendErr error
	err = raw.Write(func(socket uintptr) bool {
		for {
			_, sendErr = unix.SendmsgN(int(socket), nil, rights, nil, unix.MSG_NOSIGNAL)
			if sendErr == unix.EINTR {
				continue
			}
			return sendErr != unix.EAGAIN
		}
	})
	if err != nil {
		return err
	}
	return sendErr
}
