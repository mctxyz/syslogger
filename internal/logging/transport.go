package logging

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"
)

// 本机 IPC 使用有限等待；业务线程只负责编码和入队。
const sendTimeout = time.Second

type transport struct {
	mu      sync.Mutex
	conn    net.Conn
	network string
	path    string
	tag     string
	closed  bool
}

func (t *transport) send(ctx context.Context, entry *Entry) error {
	t.mu.Lock()
	conn, network, closed := t.conn, t.network, t.closed
	t.mu.Unlock()
	if closed {
		return context.Canceled
	}
	if conn == nil {
		paths := []string{t.path}
		if t.path == "" {
			paths = []string{"/dev/log", "/var/run/syslog", "/var/run/log"}
		}
		dialCtx, cancel := context.WithTimeout(ctx, sendTimeout)
		defer cancel()
		var err error
		for _, path := range paths {
			for _, candidate := range []string{"unixgram", "unix"} {
				conn, err = (&net.Dialer{}).DialContext(dialCtx, candidate, path)
				if err == nil {
					network = candidate
					break
				}
			}
			if err == nil {
				break
			}
		}
		if err != nil {
			return err
		}
		t.mu.Lock()
		if t.closed {
			t.mu.Unlock()
			_ = conn.Close()
			return context.Canceled
		}
		t.conn, t.network = conn, network
		t.mu.Unlock()
	}
	// 仅协议头规范化空白及控制字符，JSON 中的项目原文保持完整。
	tag := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return '_'
		}
		return r
	}, t.tag)
	data := []byte(fmt.Sprintf("<%d>%s %s[%d]: ", 8+entry.Severity, time.Now().Format(time.Stamp), tag, os.Getpid()))
	data = append(data, entry.Body...)
	if network == "unix" {
		data = append(data, '\n')
	}
	err := conn.SetWriteDeadline(time.Now().Add(sendTimeout))
	if err == nil {
		var n int
		n, err = conn.Write(data)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
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
