package syslogger

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFileFirstSnapshotRetryAndFinalOrdering(t *testing.T) {
	cfg := testConfig(t)
	cfg.File.Enabled = true
	cfg.File.OutputPath = filepath.Join(filepath.Dir(cfg.File.OutputPath), "created-later", "custom.prom")
	cfg.File.FlushInterval = 5 * time.Millisecond
	m := testManager(t, cfg)
	c := testComponent(t, m, "rpc")
	c.Degraded()
	if err := m.StartFile(); err == nil || m.filePhase != outletIdle {
		t.Fatal("首次写入失败未返回或未清理启动资格")
	}
	if err := os.Mkdir(filepath.Dir(cfg.File.OutputPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := m.StartFile(); err != nil {
		t.Fatal(err)
	}
	first := readSnapshot(t, cfg.File.OutputPath)
	if !strings.Contains(string(first), `component="rpc"} 2`) {
		t.Fatal("StartFile 返回时首次快照缺失")
	}
	if m.StartFile() == nil {
		t.Fatal("重复文件启动未失败")
	}
	c.Unhealthy()
	waitUntil(t, func() bool {
		return strings.Contains(string(readSnapshot(t, cfg.File.OutputPath)), `component="rpc"} 3`)
	})
	c.Healthy()
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	final := readSnapshot(t, cfg.File.OutputPath)
	if !strings.Contains(string(final), `component="rpc"} 1`) {
		t.Fatal("最终快照未覆盖周期快照")
	}
	time.Sleep(3 * cfg.File.FlushInterval)
	if !bytes.Equal(final, readSnapshot(t, cfg.File.OutputPath)) {
		t.Fatal("最终交付之后仍有写入")
	}
	c.Unhealthy()
	if err := m.Close(context.Background()); err != nil || !bytes.Equal(final, readSnapshot(t, cfg.File.OutputPath)) {
		t.Fatal("重复关闭改变最终结果")
	}
}

func TestUnstartedFileDoesNotWriteOnClose(t *testing.T) {
	cfg := testConfig(t)
	cfg.File.Enabled = true
	m := testManager(t, cfg)
	testComponent(t, m, "rpc").Healthy()
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(cfg.File.OutputPath))
	if err != nil || len(entries) != 0 {
		t.Fatal("关闭自行启动了未启动出口")
	}
}

func TestPeriodicFileFailureAndSharedFinalError(t *testing.T) {
	cfg := testConfig(t)
	cfg.File.Enabled = true
	cfg.File.FlushInterval = 5 * time.Millisecond
	m := testManager(t, cfg)
	c := testComponent(t, m, "rpc")
	if err := m.StartFile(); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Dir(cfg.File.OutputPath)
	moved := directory + "-offline"
	if err := os.Rename(directory, moved); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(moved)
	capture := &capturedLog{entered: make(chan struct{})}
	previous := log.Writer()
	log.SetOutput(capture)
	defer log.SetOutput(previous)
	select {
	case <-capture.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("运行期文件错误未被诊断")
	}
	c.Unhealthy()
	first := m.Close(context.Background())
	second := m.Close(context.Background())
	if first == nil || first != second || !errors.Is(first, os.ErrNotExist) {
		t.Fatalf("收尾错误未共享: %v %v", first, second)
	}
	if currentRecord(t, m, "rpc").State != 3 {
		t.Fatal("文件失败改变业务健康记录")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if m.Close(ctx) != first {
		t.Fatal("已完成结果被后续取消上下文掩盖")
	}
}

func TestFinalFileIncludesLogCloseCounters(t *testing.T) {
	path := filepath.Join(socketDirectory(t), "log.sock")
	_ = unixReceiver(t, path)
	cfg := testConfig(t)
	cfg.SyslogSocketPath = path
	cfg.File.Enabled = true
	m := testManager(t, cfg)
	c, err := m.Register("rpc", 64)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.StartFile(); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 1000; n++ {
		if errors.Is(c.Info(strings.Repeat("x", 32*1024), nil), ErrQueueFull) {
			break
		}
	}
	waitUntil(t, func() bool { _, failed := c.logs.Counts(); return failed > 0 })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_ = m.Close(ctx)
	_ = m.Close(context.Background())
	_, failed := c.logs.Counts()
	text := string(readSnapshot(t, cfg.File.OutputPath))
	line := fmt.Sprintf(`syslogger_component_log_send_failures_total{project="demo",instance_id="instance-a",component="rpc"} %d`+"\n", failed)
	if !strings.Contains(text, line) {
		t.Fatalf("最终文件未包含日志收尾计数: %s", text)
	}
}

// capturedLog 通过真实默认日志出口观察文件错误，生产代码保持直接文件调用。
type capturedLog struct {
	mu      sync.Mutex
	data    bytes.Buffer
	once    sync.Once
	entered chan struct{}
}

func (w *capturedLog) Write(data []byte) (int, error) {
	w.mu.Lock()
	_, _ = w.data.Write(data)
	w.mu.Unlock()
	w.once.Do(func() { close(w.entered) })
	return len(data), nil
}
