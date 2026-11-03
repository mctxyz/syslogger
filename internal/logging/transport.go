package logging

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/mctxyz/syslogger/internal/identity"
	"github.com/mctxyz/syslogger/internal/journalwire"
)

// 本机 IPC 使用有限等待；业务线程只负责编码和入队。
const sendTimeout = time.Second

type wireProtocol uint8

const (
	syslogDatagram wireProtocol = iota
	syslogStream
	journalNative
)

type transport struct {
	mu            sync.Mutex
	conn          net.Conn
	protocol      wireProtocol
	path          string
	id            identity.Instance
	component     string
	journalHeader []byte
	closed        bool
}

func (t *transport) send(ctx context.Context, entry *Entry) error {
	t.mu.Lock()
	conn, protocol, header, closed := t.conn, t.protocol, t.journalHeader, t.closed
	t.mu.Unlock()
	if closed {
		return context.Canceled
	}
	if conn == nil {
		dialCtx, cancel := context.WithTimeout(ctx, sendTimeout)
		defer cancel()
		var err error
		conn, protocol, err = t.connect(dialCtx)
		if err != nil {
			return err
		}
		if protocol == journalNative {
			header = journalwire.Header(t.id, t.component)
		}
		t.mu.Lock()
		if t.closed {
			t.mu.Unlock()
			_ = conn.Close()
			return context.Canceled
		}
		t.conn, t.protocol, t.journalHeader = conn, protocol, header
		t.mu.Unlock()
	}
	var data []byte
	if protocol == journalNative {
		data = journalwire.Message(header, entry.Body, uint8(entry.Severity))
	} else {
		// 传统 syslog 仅规范化协议头，JSON 中的项目原文保持完整。
		tag := identity.SyslogTag(t.id.Project)
		data = []byte(fmt.Sprintf("<%d>%s %s[%d]: ", 8+entry.Severity, time.Now().Format(time.Stamp), tag, os.Getpid()))
		data = append(data, entry.Body...)
		if protocol == syslogStream {
			data = append(data, '\n')
		}
	}
	err := conn.SetWriteDeadline(time.Now().Add(sendTimeout))
	if err == nil {
		if protocol == journalNative {
			err = writeJournal(conn.(*net.UnixConn), data)
		} else {
			var n int
			n, err = conn.Write(data)
			if err == nil && n != len(data) {
				err = io.ErrShortWrite
			}
		}
	}
	if err != nil {
		t.mu.Lock()
		if t.conn == conn {
			t.conn = nil
		}
		t.mu.Unlock()
		_ = conn.Close()
	}
	return err
}

// connect 沿用首次成功连接的系统入口；显式路径保持传统 syslog，默认 Linux 优先 journal。
func (t *transport) connect(ctx context.Context) (net.Conn, wireProtocol, error) {
	if t.path == "" && runtime.GOOS == "linux" {
		conn, err := (&net.Dialer{}).DialContext(ctx, "unixgram", journalwire.SocketPath)
		if err == nil {
			return conn, journalNative, nil
		}
	}
	paths := []string{t.path}
	if t.path == "" {
		paths = []string{"/dev/log", "/var/run/syslog", "/var/run/log"}
	}
	var lastErr error
	for _, path := range paths {
		for _, network := range []string{"unixgram", "unix"} {
			conn, err := (&net.Dialer{}).DialContext(ctx, network, path)
			if err == nil {
				protocol := syslogDatagram
				if network == "unix" {
					protocol = syslogStream
				}
				return conn, protocol, nil
			}
			lastErr = err
		}
	}
	return nil, syslogDatagram, lastErr
}

func (t *transport) close() {
	t.mu.Lock()
	t.closed = true
	conn := t.conn
	t.conn = nil
	t.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}
