package logging

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/mctxyz/syslogger/internal/journalwire"
)

// groupStreams 为各发送任务配置独立 Unix stream，接收端按组控制读取和背压。
func groupStreams(t *testing.T, q *Queue) [journalwire.GroupCount]*net.UnixConn {
	t.Helper()
	var receivers [journalwire.GroupCount]*net.UnixConn
	for group := range q.groups {
		path := syncSocketPath(t)
		address := &net.UnixAddr{Name: path, Net: "unix"}
		listener, err := net.ListenUnix("unix", address)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = listener.Close() })
		conn, err := net.DialUnix("unix", nil, address)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		if err := conn.SetWriteBuffer(4096); err != nil {
			t.Fatal(err)
		}
		receiver, err := listener.AcceptUnix()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = receiver.Close() })
		receivers[group] = receiver
		tr := &q.groups[group].transport
		tr.mu.Lock()
		tr.conn = conn
		tr.protocol = syslogStream
		tr.mu.Unlock()
	}
	return receivers
}

func TestGroupBackpressureIsolationAndFIFO(t *testing.T) {
	levels := [...]Level{Debug, Info, Warning, Error}
	for _, blocked := range []journalwire.Group{journalwire.DebugGroup, journalwire.InfoGroup} {
		t.Run(blocked.Name(), func(t *testing.T) {
			q := syncQueue(t, syncSocketPath(t), 8)
			receivers := groupStreams(t, q)
			if err := q.Enqueue(&Entry{Severity: levels[blocked], Body: bytes.Repeat([]byte("x"), 64*1024)}); err != nil {
				t.Fatal(err)
			}
			waitSyncCondition(t, func() bool { return len(q.groups[blocked].entries) == 0 })
			for index := 0; index < 8; index++ {
				if err := q.Enqueue(&Entry{Severity: levels[blocked]}); err != nil {
					t.Fatal(err)
				}
			}
			if err := q.Enqueue(&Entry{Severity: levels[blocked]}); err != ErrQueueFull {
				t.Fatalf("阻塞组没有独占队满: %v", err)
			}
			for group, level := range levels {
				if journalwire.Group(group) == blocked {
					continue
				}
				for index := 0; index < 8; index++ {
					if err := q.Enqueue(&Entry{Severity: level, Body: []byte(fmt.Sprintf("%s-%d", levelNames[level], index))}); err != nil {
						t.Fatalf("其他组受队满挤占: %v", err)
					}
				}
				_ = receivers[group].SetReadDeadline(time.Now().Add(sendTimeout / 2))
				reader := bufio.NewReader(receivers[group])
				for index := 0; index < 8; index++ {
					line, err := reader.ReadString('\n')
					want := fmt.Sprintf("%s-%d\n", levelNames[level], index)
					if err != nil || !strings.HasSuffix(line, want) {
						t.Fatalf("组 %d 未独立交付或 FIFO 被破坏: %q %v", group, line, err)
					}
				}
			}
			waitSyncCondition(t, func() bool { return q.pending.Load() == 9 })
			if len(q.groups[blocked].entries) != 8 {
				t.Fatal("隔离验证期间阻塞组已经解除背压")
			}
			if rejected, failed := q.Counts(); rejected != 1 || failed != 0 {
				t.Fatalf("组件汇总计数错误: rejected=%d failed=%d", rejected, failed)
			}
			if activity := q.TakeActivity(); activityCount(activity) != 33 {
				t.Fatalf("并行发送改变了接收活动: %+v", activity)
			}
		})
	}
}

func TestCloseCancelsAllGroupsAndSync(t *testing.T) {
	q := syncQueue(t, fullSyncSocket(t), 2)
	groupStreams(t, q)
	levels := [...]Level{Debug, Info, Warning, Error}
	for group, level := range levels {
		if err := q.Enqueue(&Entry{Severity: level, Body: bytes.Repeat([]byte("x"), 64*1024)}); err != nil {
			t.Fatal(err)
		}
		waitSyncCondition(t, func() bool { return len(q.groups[group].entries) == 0 })
		for index := 0; index < 2; index++ {
			if err := q.Enqueue(&Entry{Severity: level}); err != nil {
				t.Fatal(err)
			}
		}
	}
	sent := make(chan error, 1)
	go func() { sent <- q.SendSync(context.Background(), &Entry{Severity: Critical}) }()
	waitSyncCondition(t, func() bool { return q.pending.Load() == 13 })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	q.Stop(ctx)
	done := make(chan error, 1)
	go func() { done <- q.Wait() }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) || q.Wait().Error() != err.Error() {
			t.Fatalf("统一关闭没有复用首次期限结果: %v", err)
		}
	case <-time.After(sendTimeout / 2):
		t.Fatal("关闭未并行中断全部组和同步调用")
	}
	if err := <-sent; !errors.Is(err, context.Canceled) {
		t.Fatalf("同步发送未被组件关闭取消: %v", err)
	}
	if rejected, failed := q.Counts(); rejected != 0 || failed != 5 || q.abandoned != 8 || q.closeFailures != 5 || q.pending.Load() != 0 {
		t.Fatalf("并发收尾累计错误: rejected=%d failed=%d abandoned=%d closeFailures=%d pending=%d", rejected, failed, q.abandoned, q.closeFailures, q.pending.Load())
	}
	for group := range q.groups {
		tr := &q.groups[group].transport
		tr.mu.Lock()
		closed := tr.closed
		tr.mu.Unlock()
		if len(q.groups[group].entries) != 0 || !closed {
			t.Fatalf("组 %d 尚未完成清理", group)
		}
	}
	if activity := q.TakeActivity(); activityCount(activity) != 13 {
		t.Fatalf("四组与同步的活动未统一累计: %+v", activity)
	}
}

func TestGroupCapacityIsSharedOnlyByItsSeverities(t *testing.T) {
	q := bufferedQueue(1)
	for _, level := range []Level{Debug, Info, Warning, Error} {
		if err := q.Enqueue(&Entry{Severity: level}); err != nil {
			t.Fatalf("各组容量未独立: level=%d err=%v", level, err)
		}
	}
	for _, level := range []Level{Debug, Info, Notice, Warning, Error, Critical, Alert, Emergency} {
		if err := q.Enqueue(&Entry{Severity: level}); err != ErrQueueFull {
			t.Fatalf("同组等级没有共用容量: level=%d err=%v", level, err)
		}
	}
}

func TestSharedDatagramBackpressureStillStartsEveryGroup(t *testing.T) {
	q := syncQueue(t, fullSyncSocket(t), 1)
	levels := [...]Level{Debug, Info, Warning, Error}
	for _, level := range levels {
		if err := q.Enqueue(&Entry{Severity: level}); err != nil {
			t.Fatal(err)
		}
	}
	for group := range q.groups {
		waitSyncCondition(t, func() bool {
			tr := &q.groups[group].transport
			tr.mu.Lock()
			defer tr.mu.Unlock()
			return tr.conn != nil && len(q.groups[group].entries) == 0
		})
	}
	for _, level := range levels {
		if err := q.Enqueue(&Entry{Severity: level}); err != nil {
			t.Fatalf("公共后端背压阻止了其他组入队: %v", err)
		}
		if err := q.Enqueue(&Entry{Severity: level}); err != ErrQueueFull {
			t.Fatalf("组容量边界错误: %v", err)
		}
	}
	if rejected, failed := q.Counts(); rejected != 4 || failed != 0 || q.pending.Load() != 8 {
		t.Fatalf("各组未独立发起发送并保留积压: rejected=%d failed=%d pending=%d", rejected, failed, q.pending.Load())
	}
}

func TestExplicitSyslogUsesFourIndependentConnections(t *testing.T) {
	path := syncSocketPath(t)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	_ = listener.SetDeadline(time.Now().Add(5 * time.Second))
	q := syncQueue(t, path, 1)
	for _, level := range []Level{Debug, Info, Warning, Error} {
		if err := q.Enqueue(&Entry{Severity: level, Body: []byte(levelNames[level])}); err != nil {
			t.Fatal(err)
		}
	}
	seen := make(map[string]bool)
	for range q.groups {
		conn, err := listener.AcceptUnix()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		line, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		name := line[strings.LastIndexByte(line, ' ')+1:]
		if seen[name] {
			t.Fatalf("一个等级占用了其他组连接: %q", name)
		}
		seen[name] = true
	}
	for _, name := range []string{"debug\n", "info\n", "warning\n", "error\n"} {
		if !seen[name] {
			t.Fatalf("遗漏等级组独立连接: %q", name)
		}
	}
	q.Stop(context.Background())
	if err := q.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestCloseWaitsForLastGroupWithoutCancelingIt(t *testing.T) {
	q := syncQueue(t, syncSocketPath(t), 1)
	receivers := groupStreams(t, q)
	body := strings.Repeat("x", 64*1024)
	if err := q.Enqueue(&Entry{Severity: Error, Body: []byte(body)}); err != nil {
		t.Fatal(err)
	}
	waitSyncCondition(t, func() bool { return len(q.groups[journalwire.ErrorGroup].entries) == 0 })
	q.Stop(context.Background())
	// 等待其他空组退出，最后一组仍必须能在正常排空时交付。
	for group := journalwire.DebugGroup; group < journalwire.ErrorGroup; group++ {
		waitSyncCondition(t, func() bool {
			tr := &q.groups[group].transport
			tr.mu.Lock()
			defer tr.mu.Unlock()
			return tr.closed
		})
	}
	select {
	case <-q.done:
		t.Fatal("空组提前结束了组件")
	default:
	}
	if err := q.ctx.Err(); err != nil {
		t.Fatalf("空组取消了最后一组: %v", err)
	}
	_ = receivers[journalwire.ErrorGroup].SetReadDeadline(time.Now().Add(sendTimeout))
	line, err := bufio.NewReader(receivers[journalwire.ErrorGroup]).ReadString('\n')
	if err != nil || !strings.HasSuffix(line, body+"\n") {
		t.Fatalf("最后一组没有完成交付: %v", err)
	}
	if err := q.Wait(); err != nil {
		t.Fatal(err)
	}
}
