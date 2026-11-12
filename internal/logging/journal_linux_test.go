package logging

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mctxyz/syslogger/internal/identity"
	"github.com/mctxyz/syslogger/internal/journalwire"
	"golang.org/x/sys/unix"
)

func journalSocketPair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	directory, err := os.MkdirTemp("", "sj-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	path := filepath.Join(directory, "socket")
	receiver, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = receiver.Close() })
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn, receiver
}

func TestJournalSmallMessageAndLargeMemfd(t *testing.T) {
	for _, size := range []int{128, 512 * 1024} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			conn, receiver := journalSocketPair(t)
			if err := conn.SetWriteBuffer(32 * 1024); err != nil {
				t.Fatal(err)
			}
			id := identity.Instance{Project: "p", InstanceID: "i"}
			tr := &transport{
				conn:          conn,
				group:         journalwire.ErrorGroup,
				protocol:      journalNative,
				id:            id,
				component:     "rpc",
				journalHeader: journalwire.Header(id, "rpc"),
			}
			entry := &Entry{Severity: Error, Body: []byte(strings.Repeat("x", size))}
			buffer, err := tr.send(context.Background(), entry, nil)
			if err != nil {
				t.Fatal(err)
			}
			if size > maxRetainedSendBuffer && buffer != nil || size < maxRetainedSendBuffer && len(buffer) == 0 {
				t.Fatal("正常发送后的大消息缓冲被长期保留，或小消息未交还复用")
			}
			_ = receiver.SetReadDeadline(time.Now().Add(5 * time.Second))
			data := make([]byte, 64*1024)
			control := make([]byte, unix.CmsgSpace(4))
			n, controlN, flags, _, err := receiver.ReadMsgUnix(data, control)
			if err != nil || flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 {
				t.Fatalf("接收原生消息失败: %d, %v", flags, err)
			}
			if size < 1024 {
				if controlN != 0 || !bytes.Equal(data[:n], journalwire.AppendMessage(nil, tr.journalHeader, entry.Body, uint8(entry.Severity))) {
					t.Fatal("小消息未通过单次原生数据报完整交付")
				}
				return
			}
			messages, err := unix.ParseSocketControlMessage(control[:controlN])
			if err != nil || len(messages) != 1 || n != 0 {
				t.Fatalf("大消息未通过单个文件描述符交付: %d, %v", n, err)
			}
			fds, err := unix.ParseUnixRights(&messages[0])
			if err != nil || len(fds) != 1 {
				t.Fatalf("大消息描述符不符: %v, %v", fds, err)
			}
			file := os.NewFile(uintptr(fds[0]), "journal-test")
			defer file.Close()
			if _, err := file.Seek(0, io.SeekStart); err != nil {
				t.Fatal(err)
			}
			actual, err := io.ReadAll(file)
			if err != nil || !bytes.Equal(actual, journalwire.AppendMessage(nil, tr.journalHeader, entry.Body, uint8(entry.Severity))) {
				t.Fatalf("匿名文件中的大消息不完整: %v", err)
			}
			seals, err := unix.FcntlInt(file.Fd(), unix.F_GET_SEALS, 0)
			if err != nil || seals&unix.F_SEAL_WRITE == 0 {
				t.Fatalf("消息文件未密封: %d, %v", seals, err)
			}
			entry = &Entry{Severity: Critical, Body: []byte("after large message")}
			buffer, err = tr.send(context.Background(), entry, buffer)
			if err != nil || len(buffer) == 0 || cap(buffer) > maxRetainedSendBuffer {
				t.Fatalf("大消息之后未恢复常规缓冲: %v", err)
			}
			n, err = receiver.Read(data)
			if err != nil || !bytes.Equal(data[:n], buffer) {
				t.Fatalf("大消息之后的普通数据报被污染: %v", err)
			}
		})
	}
}

func TestJournalCloseInterruptsBackpressure(t *testing.T) {
	for _, size := range []int{32 * 1024, 512 * 1024} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			conn, _ := journalSocketPair(t)
			if err := conn.SetWriteBuffer(32 * 1024); err != nil {
				t.Fatal(err)
			}
			id := identity.Instance{Project: "p", InstanceID: "i"}
			q := syncQueue(t, "", 64)
			tr := &q.groups[journalwire.InfoGroup].transport
			tr.mu.Lock()
			tr.conn = conn
			tr.protocol = journalNative
			tr.journalHeader = journalwire.Header(id, "rpc")
			tr.mu.Unlock()
			entry := &Entry{Severity: Info, Body: bytes.Repeat([]byte("x"), size)}
			for index := 0; index < 1000; index++ {
				if errors.Is(q.Enqueue(entry), ErrQueueFull) {
					break
				}
			}
			closing, stop := context.WithTimeout(context.Background(), 10*time.Millisecond)
			defer stop()
			q.Stop(closing)
			done := make(chan error, 1)
			go func() { done <- q.Wait() }()
			select {
			case err := <-done:
				if !errors.Is(err, context.DeadlineExceeded) || q.pending.Load() != 0 {
					t.Fatalf("原生出口关闭未中断积压交付: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("原生写入未被关闭中断")
			}
		})
	}
}

func TestJournalWriteDeadline(t *testing.T) {
	for _, size := range []int{32 * 1024, 512 * 1024} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			conn, _ := journalSocketPair(t)
			if err := conn.SetWriteBuffer(32 * 1024); err != nil {
				t.Fatal(err)
			}
			tr := &transport{conn: conn, group: journalwire.InfoGroup, protocol: journalNative}
			entry := &Entry{Severity: Info, Body: bytes.Repeat([]byte("x"), size)}
			var buffer []byte
			for index := 0; index < 100; index++ {
				var err error
				buffer, err = tr.send(context.Background(), entry, buffer)
				if err != nil {
					var timedOut net.Error
					if !errors.As(err, &timedOut) || !timedOut.Timeout() || tr.conn != nil {
						t.Fatalf("背压未按写入截止失败并释放连接: %v", err)
					}
					return
				}
			}
			t.Fatal("没有形成可验证的原生 socket 背压")
		})
	}
}

func TestJournalGroupConnectionsAndFailureIsolation(t *testing.T) {
	id := identity.Instance{Project: "project", InstanceID: "main"}
	q := syncQueue(t, "", 8)
	var receivers [journalwire.GroupCount]*net.UnixConn
	for group := journalwire.DebugGroup; group < journalwire.GroupCount; group++ {
		tr := &q.groups[group].transport
		tr.mu.Lock()
		tr.conn, receivers[group] = journalSocketPair(t)
		tr.protocol = journalNative
		tr.journalHeader = journalwire.Header(id, "rpc")
		tr.mu.Unlock()
	}
	for severity := Emergency; severity <= Debug; severity++ {
		entry := &Entry{Severity: severity, Body: []byte(levelNames[severity])}
		if err := q.Enqueue(entry); err != nil {
			t.Fatal(err)
		}
		group := journalwire.GroupForSeverity(uint8(severity))
		if err := receivers[group].SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		data := make([]byte, 4096)
		n, err := receivers[group].Read(data)
		if err != nil || !bytes.Equal(data[:n], journalwire.AppendMessage(nil, journalwire.Header(id, "rpc"), entry.Body, uint8(severity))) {
			t.Fatalf("等级 %d 未保留正文与身份并投递到目标组: %v", severity, err)
		}
	}
	waitSyncCondition(t, func() bool { return q.pending.Load() == 0 })
	debug := &q.groups[journalwire.DebugGroup].transport
	debug.mu.Lock()
	debugConn := debug.conn
	debug.mu.Unlock()
	if err := debugConn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(&Entry{Severity: Debug}); err != nil {
		t.Fatal(err)
	}
	waitSyncCondition(t, func() bool {
		_, failed := q.Counts()
		return failed == 1 && q.pending.Load() == 0
	})
	debug.mu.Lock()
	released := debug.conn == nil
	debug.mu.Unlock()
	if !released {
		t.Fatal("失败组未释放连接")
	}
	for group := journalwire.InfoGroup; group < journalwire.GroupCount; group++ {
		tr := &q.groups[group].transport
		tr.mu.Lock()
		connected := tr.conn != nil
		tr.mu.Unlock()
		if !connected {
			t.Fatalf("错误关闭了 %s 空间连接", group.Name())
		}
	}
	if err := q.Enqueue(&Entry{Severity: Error}); err != nil {
		t.Fatalf("其他组受单组失败影响: %v", err)
	}
	var connections [journalwire.GroupCount]net.Conn
	for group := range q.groups {
		tr := &q.groups[group].transport
		tr.mu.Lock()
		connections[group] = tr.conn
		tr.mu.Unlock()
	}
	q.Stop(context.Background())
	if err := q.Wait(); err != nil {
		t.Fatal(err)
	}
	for group, conn := range connections {
		if conn != nil && conn.SetWriteDeadline(time.Now()) == nil {
			t.Fatalf("关闭遗漏了组 %d 的连接", group)
		}
	}
}
