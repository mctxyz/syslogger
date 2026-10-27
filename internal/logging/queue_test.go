package logging

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestQueueFullPreservesFIFO(t *testing.T) {
	q := &Queue{entries: make(chan *Entry, 2)}
	a, b, c := &Entry{Body: []byte("a")}, &Entry{Body: []byte("b")}, &Entry{Body: []byte("c")}
	if q.Enqueue(a) != nil || q.Enqueue(b) != nil || !errors.Is(q.Enqueue(c), ErrQueueFull) {
		t.Fatal("容量拒收不符")
	}
	if <-q.entries != a || <-q.entries != b {
		t.Fatal("拒收覆盖了已有 FIFO 记录")
	}
	if rejected, failed := q.Counts(); rejected != 1 || failed != 0 {
		t.Fatal("拒收与发送失败计数混淆")
	}
}

func TestQueueCapacity(t *testing.T) {
	for _, capacity := range []int{1, 64, 512, 4095, 4096} {
		q := New(capacity, "", "demo", "common", "rpc")
		if cap(q.entries) != capacity {
			t.Fatalf("容量 %d 实际为 %d", capacity, cap(q.entries))
		}
		q.Stop(context.Background())
		if err := q.Wait(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCloseInterruptsBackpressureAndReleasesQueue(t *testing.T) {
	dir, err := os.MkdirTemp("", "slg-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "log.sock")
	receiver, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	q := New(64, path, "demo", "common", "rpc")
	entry := &Entry{Severity: Info, Body: []byte(strings.Repeat("x", 32*1024))}
	for i := 0; i < 1000; i++ {
		if errors.Is(q.Enqueue(entry), ErrQueueFull) {
			break
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	q.Stop(ctx)
	done := make(chan error, 1)
	go func() { done <- q.Wait() }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("关闭未反馈未完成交付: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("关闭未中断 socket 写入")
	}
	if len(q.entries) != 0 || q.pending.Load() != 0 || q.abandoned == 0 {
		t.Fatal("关闭未释放积压记录")
	}
	if !errors.Is(q.Enqueue(entry), ErrClosed) {
		t.Fatal("关闭后继续接收")
	}
}

func TestUnixStreamFallbackAndFraming(t *testing.T) {
	dir, err := os.MkdirTemp("", "slg-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "log.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	q := New(8, path, "demo", "common", "rpc")
	if err := q.Enqueue(&Entry{Severity: Error, Body: []byte(`{"message":"first"}`)}); err != nil {
		t.Fatal(err)
	}
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "<11>") || !strings.HasSuffix(line, "}\n") {
		t.Fatalf("流式封装不符: %q %v", line, err)
	}
	q.Stop(context.Background())
	if err := q.Wait(); err != nil {
		t.Fatal(err)
	}
}
