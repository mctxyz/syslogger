package syslogger

import (
	"bytes"
	"context"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mctxyz/syslogger/internal/record"
	"github.com/prometheus/common/expfmt"
)

// capturedLog 同步捕获标准日志，供各类诊断测试共享。
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
	w.once.Do(func() {
		close(w.entered)
	})
	return len(data), nil
}

// String 在后台诊断可能仍写入时读取完整的测试日志。
func (w *capturedLog) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.data.String()
}

func captureLogs(t *testing.T) *capturedLog {
	t.Helper()
	capture := &capturedLog{
		entered: make(chan struct{}),
	}
	previous := log.Writer()
	log.SetOutput(capture)
	t.Cleanup(func() {
		log.SetOutput(previous)
	})
	return capture
}

// saturateLogQueue 通过隔离 socket 的真实背压触发下一次队满，并读取该次窗口起点。
func saturateLogQueue(t *testing.T, c *Component) time.Time {
	t.Helper()
	before, _ := c.logs.Counts()
	message := strings.Repeat("x", 32*1024)
	for n := 0; n < 1000; n++ {
		c.Info(message, nil)
		if rejected, _ := c.logs.Counts(); rejected > before {
			c.mu.RLock()
			droppedAt := c.lastQueueFull
			c.mu.RUnlock()
			return droppedAt
		}
	}
	t.Fatal("未形成可验证的队满丢弃")
	return time.Time{}
}

func testConfig(t *testing.T) Config {
	t.Helper()
	return Config{Project: "demo", InstanceID: "instance-a", StateEnabled: true, SyslogSocketPath: filepath.Join(t.TempDir(), "missing.sock"), File: FileConfig{OutputPath: filepath.Join(t.TempDir(), "metrics.prom"), FlushInterval: time.Hour}}
}

func testManager(t *testing.T, cfg Config) *Manager {
	t.Helper()
	m, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = m.Close(ctx)
	})
	return m
}

func testComponent(t *testing.T, m *Manager, name string) *Component {
	t.Helper()
	c, err := m.Register(ComponentConfig{Name: name})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func currentRecord(t *testing.T, m *Manager, name string) record.Record {
	t.Helper()
	for _, item := range m.snapshot() {
		if item.Name == name {
			return item.Record
		}
	}
	t.Fatalf("缺失组件 %q", name)
	return record.Record{}
}

func waitUntil(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("等待后台处理超时")
		}
		time.Sleep(time.Millisecond)
	}
}

func readSnapshot(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// hasMetricSample 按指标名及完整标签集合匹配数值，忽略官方编码的标签顺序和数字表示。
func hasMetricSample(t testing.TB, text, name string, labels map[string]string, value float64) bool {
	t.Helper()
	var parser expfmt.TextParser
	families, err := parser.TextToMetricFamilies(strings.NewReader(text))
	if err != nil {
		t.Fatalf("指标文本无效: %v", err)
	}
	family := families[name]
	if family == nil {
		return false
	}
	for _, metric := range family.Metric {
		if len(metric.Label) != len(labels) {
			continue
		}
		matches := true
		for _, label := range metric.Label {
			wanted, exists := labels[label.GetName()]
			if !exists || label.GetValue() != wanted {
				matches = false
				break
			}
		}
		if !matches {
			continue
		}
		if metric.Gauge != nil {
			return metric.GetGauge().GetValue() == value
		}
		if metric.Counter != nil {
			return metric.GetCounter().GetValue() == value
		}
		t.Fatalf("指标 %s 不是 Gauge 或 Counter", name)
	}
	return false
}

func socketDirectory(t testing.TB) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "slg-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func unixReceiver(t testing.TB, path string) *net.UnixConn {
	t.Helper()
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func receive(t *testing.T, conn *net.UnixConn) []byte {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	data := make([]byte, 65536)
	n, _, err := conn.ReadFromUnix(data)
	if err != nil {
		t.Fatal(err)
	}
	return data[:n]
}
