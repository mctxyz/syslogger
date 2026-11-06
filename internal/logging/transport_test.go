package logging

import (
	"context"
	"errors"
	"net"
	"runtime"
	"testing"

	"github.com/mctxyz/syslogger/internal/journalwire"
)

func TestDefaultJournalNamespaceDoesNotFallBack(t *testing.T) {
	for group := journalwire.DebugGroup; group < journalwire.GroupCount; group++ {
		t.Run(group.Name(), func(t *testing.T) {
			// 预先取消会在实际连接前返回，检查默认目标时不接触真实日志服务。
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			conn, protocol, err := (&transport{}).connect(ctx, group)
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
	conn, protocol, err := (&transport{path: path}).connect(context.Background(), journalwire.DebugGroup)
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
