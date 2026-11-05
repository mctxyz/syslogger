package logging

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func syncSocketPath(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("", "sls-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	return filepath.Join(directory, "socket")
}

func syncQueue(t *testing.T, path string, capacity int) *Queue {
	t.Helper()
	q := New(capacity, path, "p", "i", "rpc", make(chan struct{}, 1))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		q.Stop(ctx)
		_ = q.Wait()
	})
	return q
}

func waitSyncCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("同步交付未进入预期状态")
		}
		time.Sleep(time.Millisecond)
	}
}

// fullSyncSocket 使用独立连接逐条填满接收队列，调用方保持该接收端不读取。
// 复用单个填充连接可能先耗尽其发送缓冲，无法阻塞后续同步发送使用的新连接。
func fullSyncSocket(t *testing.T) string {
	t.Helper()
	path := syncSocketPath(t)
	address := &net.UnixAddr{Name: path, Net: "unixgram"}
	receiver, err := net.ListenUnixgram("unixgram", address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = receiver.Close() })
	// 小数据报填满接收条数，避免仅因大包空间不足而仍允许后续小日志写入。
	data := []byte("fill")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		filler, err := net.DialUnix("unixgram", nil, address)
		if err != nil {
			t.Fatal(err)
		}
		if err := filler.SetWriteDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
			_ = filler.Close()
			t.Fatal(err)
		}
		_, err = filler.Write(data)
		_ = filler.Close()
		if err != nil {
			var timedOut net.Error
			if !errors.As(err, &timedOut) || !timedOut.Timeout() {
				t.Fatal(err)
			}
			return path
		}
	}
	t.Fatal("隔离 socket 未形成接收背压")
	return ""
}

func TestSyncBypassesBlockedFIFO(t *testing.T) {
	path := syncSocketPath(t)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	asyncConn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = asyncConn.Close() })
	if err := asyncConn.SetWriteBuffer(4096); err != nil {
		t.Fatal(err)
	}
	blocked, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = blocked.Close() })
	q := syncQueue(t, path, 1)
	q.transport.mu.Lock()
	q.transport.conn = asyncConn
	q.transport.protocol = syslogStream
	q.transport.mu.Unlock()
	if err := q.Enqueue(&Entry{Body: bytes.Repeat([]byte("x"), 64*1024)}); err != nil {
		t.Fatal(err)
	}
	waitSyncCondition(t, func() bool { return len(q.entries) == 0 })
	if err := q.Enqueue(&Entry{Body: []byte("queued")}); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(&Entry{Body: []byte("rejected")}); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("普通队列没有形成积压: %v", err)
	}
	if err := q.SendSync(context.Background(), &Entry{Severity: Critical, Body: []byte("immediate")}); err != nil {
		t.Fatalf("同步发送等待了普通队列: %v", err)
	}
	conn, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "<10>") || !strings.HasSuffix(line, "immediate\n") {
		t.Fatalf("同步独立连接或流式封装错误: %q, %v", line, err)
	}
	if q.pending.Load() != 2 || len(q.entries) != 1 {
		t.Fatal("同步发送消费或等待了普通积压")
	}
	if activity := q.TakeActivity(); activity.Count != 3 {
		t.Fatalf("异步与同步接收量没有共用同一活动来源: %+v", activity)
	}
}

func TestSyncContextInterruptsBackpressure(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			q := syncQueue(t, fullSyncSocket(t), 1)
			var ctx context.Context
			var cancel context.CancelFunc
			if mode == "deadline" {
				ctx, cancel = context.WithTimeout(context.Background(), 40*time.Millisecond)
			} else {
				ctx, cancel = context.WithCancel(context.Background())
			}
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- q.SendSync(ctx, &Entry{Body: []byte("blocked")}) }()
			waitSyncCondition(t, func() bool { return q.pending.Load() == 1 })
			if mode == "cancel" {
				cancel()
			}
			select {
			case err := <-done:
				if !errors.Is(err, ctx.Err()) {
					t.Fatalf("调用取消原因被连接错误掩盖: %v", err)
				}
			case <-time.After(sendTimeout / 2):
				t.Fatal("上下文没有提前中断一秒写入等待")
			}
			if rejected, failed := q.Counts(); rejected != 0 || failed != 1 || q.pending.Load() != 0 {
				t.Fatalf("发送中断的计数或完成状态错误: rejected=%d failed=%d", rejected, failed)
			}
		})
	}
}

func TestCloseWaitsForAndCancelsSynchronousSend(t *testing.T) {
	q := syncQueue(t, fullSyncSocket(t), 1)
	sent := make(chan error, 1)
	go func() { sent <- q.SendSync(context.Background(), &Entry{Body: []byte("blocked")}) }()
	waitSyncCondition(t, func() bool { return q.pending.Load() == 1 })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	q.Stop(ctx)
	done := make(chan error, 1)
	go func() { done <- q.Wait() }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("关闭没有等待已接收的同步发送: %v", err)
		}
	case <-time.After(sendTimeout / 2):
		t.Fatal("关闭期限没有取消同步连接")
	}
	if err := <-sent; !errors.Is(err, context.Canceled) {
		t.Fatalf("同步调用没有反馈关闭取消: %v", err)
	}
	if rejected, failed := q.Counts(); rejected != 0 || failed != 1 || q.pending.Load() != 0 {
		t.Fatalf("关闭未完成同步交付的计数错误: rejected=%d failed=%d", rejected, failed)
	}
	if err := q.SendSync(context.Background(), &Entry{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("关闭之后接收了同步调用: %v", err)
	}
}

func TestConcurrentSyncAdmissionAndClose(t *testing.T) {
	q := syncQueue(t, fullSyncSocket(t), 1)
	const count = 32
	start := make(chan struct{})
	var callers sync.WaitGroup
	for index := 0; index < count; index++ {
		callers.Add(1)
		go func() {
			defer callers.Done()
			<-start
			_ = q.SendSync(context.Background(), &Entry{Body: []byte("racing")})
		}()
	}
	callers.Add(1)
	go func() {
		defer callers.Done()
		<-start
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		q.Stop(ctx)
		_ = q.Wait()
	}()
	close(start)
	callers.Wait()
	rejected, failed := q.Counts()
	if activity := q.TakeActivity(); rejected+activity.Count != count || failed != activity.Count || q.pending.Load() != 0 {
		t.Fatalf("关闭竞争丢失接收、失败或拒收: rejected=%d failed=%d activity=%+v", rejected, failed, activity)
	}
}
