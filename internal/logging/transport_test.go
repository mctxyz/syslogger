package logging

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mctxyz/syslogger/internal/identity"
	"github.com/mctxyz/syslogger/internal/journalwire"
)

// connectedTransport 使用独立 Unix socket 验证真实写入，按协议提供可控接收端。
func connectedTransport(t testing.TB, protocol wireProtocol) (*transport, net.Conn) {
	t.Helper()
	directory, err := os.MkdirTemp("", "slt-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	path := filepath.Join(directory, "socket")
	network := "unixgram"
	if protocol == syslogStream {
		network = "unix"
	}
	address := &net.UnixAddr{Name: path, Net: network}
	var receiver net.Conn
	var sender *net.UnixConn
	if network == "unix" {
		listener, err := net.ListenUnix(network, address)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = listener.Close() })
		sender, err = net.DialUnix(network, nil, address)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = sender.Close() })
		receiver, err = listener.AcceptUnix()
		if err != nil {
			t.Fatal(err)
		}
	} else {
		receiver, err = net.ListenUnixgram(network, address)
		if err != nil {
			t.Fatal(err)
		}
		sender, err = net.DialUnix(network, nil, address)
		if err != nil {
			_ = receiver.Close()
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = receiver.Close() })
	id := identity.Instance{Project: "bench", InstanceID: "common"}
	tr := &transport{
		conn:          sender,
		protocol:      protocol,
		path:          path,
		id:            id,
		component:     "rpc",
		journalHeader: journalwire.Header(id, "rpc"),
	}
	t.Cleanup(tr.close)
	return tr, receiver
}

func TestDefaultJournalNamespaceDoesNotFallBack(t *testing.T) {
	for group := journalwire.DebugGroup; group < journalwire.GroupCount; group++ {
		t.Run(group.Name(), func(t *testing.T) {
			// 预先取消会在实际连接前返回，检查默认目标时不接触真实日志服务。
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			conn, protocol, err := (&transport{group: group}).connect(ctx)
			if conn != nil {
				_ = conn.Close()
				t.Fatal("取消的默认连接仍返回了连接")
			}
			if protocol != journalNative || err == nil {
				t.Fatalf("默认出口未保持专用原生协议：protocol=%d err=%v", protocol, err)
			}
			if runtime.GOOS != "linux" {
				return
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("默认连接未保留取消原因：%v", err)
			}
			var dialErr *net.OpError
			if !errors.As(err, &dialErr) || dialErr.Addr == nil || dialErr.Net != "unixgram" {
				t.Fatalf("默认连接缺少原生 socket 目标：%v", err)
			}
			if got := dialErr.Addr.String(); got != group.SocketPath() {
				t.Fatalf("默认连接改投了其他入口：%q", got)
			}
		})
	}
}

func TestExplicitSyslogFailureKeepsConfiguredPath(t *testing.T) {
	path := syncSocketPath(t)
	conn, protocol, err := (&transport{path: path}).connect(context.Background())
	if conn != nil {
		_ = conn.Close()
		t.Fatal("不存在的显式 socket 仍返回了连接")
	}
	var dialErr *net.OpError
	if protocol != syslogDatagram || !errors.As(err, &dialErr) || dialErr.Addr == nil {
		t.Fatalf("显式 socket 失败未保留传统协议错误：protocol=%d err=%v", protocol, err)
	}
	if got := dialErr.Addr.String(); got != path {
		t.Fatalf("显式 socket 失败后改投了其他入口：%q", got)
	}
}

func TestTransportReusesBufferWithoutMutatingInputs(t *testing.T) {
	for _, protocol := range []wireProtocol{journalNative, syslogDatagram, syslogStream} {
		t.Run(fmt.Sprint(protocol), func(t *testing.T) {
			if protocol == journalNative && runtime.GOOS != "linux" {
				t.Skip("原生 journal 发送需要 Linux")
			}
			tr, receiver := connectedTransport(t, protocol)
			header := bytes.Clone(tr.journalHeader)
			buffer := make([]byte, 0, 8192)
			start := &buffer[:cap(buffer)][0]
			for index, size := range []int{4096, 32, 1024} {
				entry := &Entry{Severity: Level(index), Body: bytes.Repeat([]byte{byte('a' + index)}, size)}
				body := bytes.Clone(entry.Body)
				var err error
				buffer, err = tr.send(context.Background(), entry, buffer)
				if err != nil {
					t.Fatal(err)
				}
				if &buffer[0] != start || cap(buffer) > maxRetainedSendBuffer {
					t.Fatal("常规发送没有复用原有缓冲")
				}
				_ = receiver.SetReadDeadline(time.Now().Add(5 * time.Second))
				actual := make([]byte, len(buffer)+1)
				var n int
				if protocol == syslogStream {
					n, err = io.ReadFull(receiver, actual[:len(buffer)])
				} else {
					n, err = receiver.Read(actual)
				}
				if err != nil || !bytes.Equal(actual[:n], buffer) {
					t.Fatalf("缓冲复用污染了实际协议消息: %v", err)
				}
				if !bytes.Equal(tr.journalHeader, header) || !bytes.Equal(entry.Body, body) {
					t.Fatal("缓冲复用覆盖了固定头或原始正文")
				}
				if protocol == journalNative {
					want := journalwire.AppendMessage(nil, header, body, uint8(entry.Severity))
					if !bytes.Equal(buffer, want) {
						t.Fatal("原生消息包含上条日志的尾部")
					}
				} else {
					want := fmt.Sprintf("<%d>", 8+entry.Severity)
					suffix := string(body)
					if protocol == syslogStream {
						suffix += "\n"
					}
					if !strings.HasPrefix(string(buffer), want) || !strings.HasSuffix(string(buffer), suffix) {
						t.Fatal("传统 syslog 等级或正文被改变")
					}
				}
			}
		})
	}
}

func TestTransportWriteFailureReleasesLargeBufferAndReconnects(t *testing.T) {
	for _, size := range []int{128, 2 * maxRetainedSendBuffer} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			tr, receiver := connectedTransport(t, syslogDatagram)
			if err := tr.conn.Close(); err != nil {
				t.Fatal(err)
			}
			buffer, err := tr.send(context.Background(), &Entry{Severity: Info, Body: bytes.Repeat([]byte("x"), size)}, make([]byte, 0, 8192))
			if err == nil || tr.conn != nil {
				t.Fatalf("发送失败未释放连接: %v", err)
			}
			if size > maxRetainedSendBuffer && buffer != nil || size < maxRetainedSendBuffer && cap(buffer) != 8192 {
				t.Fatal("发送失败后的缓冲留存范围错误")
			}
			buffer, err = tr.send(context.Background(), &Entry{Severity: Error, Body: []byte("after reconnect")}, buffer)
			if err != nil || cap(buffer) > maxRetainedSendBuffer {
				t.Fatalf("后续重连未恢复发送: %v", err)
			}
			_ = receiver.SetReadDeadline(time.Now().Add(5 * time.Second))
			data := make([]byte, 1024)
			n, err := receiver.Read(data)
			if err != nil || !bytes.Equal(data[:n], buffer) || !bytes.HasSuffix(data[:n], []byte("after reconnect")) {
				t.Fatalf("重连补发了旧消息或污染了正文: %v", err)
			}
		})
	}
}

func TestTransportConnectFailureAndCloseKeepCallerBuffer(t *testing.T) {
	tr := &transport{path: syncSocketPath(t)}
	buffer := make([]byte, 4, 256)
	copy(buffer, "keep")
	got, err := tr.send(context.Background(), &Entry{}, buffer)
	if err == nil || len(got) != len(buffer) || &got[0] != &buffer[0] || !bytes.Equal(got, buffer) {
		t.Fatalf("连接失败覆盖了调用方缓冲: %v", err)
	}
	tr.close()
	got, err = tr.send(context.Background(), &Entry{}, buffer)
	if !errors.Is(err, context.Canceled) || len(got) != len(buffer) || &got[0] != &buffer[0] || !bytes.Equal(got, buffer) {
		t.Fatalf("关闭访问了调用方缓冲: %v", err)
	}
}
