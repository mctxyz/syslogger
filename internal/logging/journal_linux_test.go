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
			tr := &transport{conn: conn, protocol: journalNative, id: id, component: "rpc", journalHeader: journalwire.Header(id, "rpc")}
			entry := &Entry{Severity: Error, Body: []byte(strings.Repeat("x", size))}
			if err := tr.send(context.Background(), entry); err != nil {
				t.Fatal(err)
			}
			_ = receiver.SetReadDeadline(time.Now().Add(5 * time.Second))
			data := make([]byte, 64*1024)
			control := make([]byte, unix.CmsgSpace(4))
			n, controlN, flags, _, err := receiver.ReadMsgUnix(data, control)
			if err != nil || flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 {
				t.Fatalf("接收原生消息失败: %d, %v", flags, err)
			}
			if size < 1024 {
				if controlN != 0 || !bytes.Equal(data[:n], journalwire.Message(tr.journalHeader, entry.Body, uint8(entry.Severity))) {
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
			if err != nil || !bytes.Equal(actual, journalwire.Message(tr.journalHeader, entry.Body, uint8(entry.Severity))) {
				t.Fatalf("匿名文件中的大消息不完整: %v", err)
			}
			seals, err := unix.FcntlInt(file.Fd(), unix.F_GET_SEALS, 0)
			if err != nil || seals&unix.F_SEAL_WRITE == 0 {
				t.Fatalf("消息文件未密封: %d, %v", seals, err)
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
			ctx, cancel := context.WithCancel(context.Background())
			q := &Queue{
				entries:   make(chan *Entry, 64),
				done:      make(chan struct{}),
				ctx:       ctx,
				cancel:    cancel,
				transport: transport{conn: conn, protocol: journalNative, id: id, component: "rpc", journalHeader: journalwire.Header(id, "rpc")},
			}
			go q.run()
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
			tr := &transport{conn: conn, protocol: journalNative}
			entry := &Entry{Severity: Info, Body: bytes.Repeat([]byte("x"), size)}
			for index := 0; index < 100; index++ {
				err := tr.send(context.Background(), entry)
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
