package syslogger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEightLevelsAndFieldSnapshot(t *testing.T) {
	path := filepath.Join(socketDirectory(t), "log.sock")
	receiver := unixReceiver(t, path)
	cfg := testConfig(t)
	cfg.SyslogSocketPath = path
	cfg.StateEnabled = false
	m := testManager(t, cfg)
	c := testComponent(t, m, "rpc")
	methods := []func(string, map[string]any) error{c.Emergency, c.Alert, c.Critical, c.Error, c.Warning, c.Notice, c.Info, c.Debug}
	names := []string{"emergency", "alert", "critical", "error", "warning", "notice", "info", "debug"}
	for level, method := range methods {
		fields := map[string]any{"error": errors.New("原始错误"), "nested": map[string]any{"value": "原值"}}
		if err := method("消息\n第二行", fields); err != nil {
			t.Fatal(err)
		}
		fields["error"] = "已改变"
		fields["nested"].(map[string]any)["value"] = "已改变"
		data := receive(t, receiver)
		if !strings.HasPrefix(string(data), fmt.Sprintf("<%d>", 8+level)) || strings.Contains(string(data), "\n") {
			t.Fatalf("等级或单行格式不符: %s", data)
		}
		start := strings.IndexByte(string(data), '{')
		var event struct {
			Timestamp                          time.Time `json:"timestamp"`
			Level, Project, Component, Message string
			InstanceID                         string `json:"instance_id"`
			Fields                             map[string]any
		}
		if start < 0 || json.Unmarshal(data[start:], &event) != nil {
			t.Fatalf("JSON 无效: %s", data)
		}
		if event.Level != names[level] || event.Project != cfg.Project || event.InstanceID != cfg.InstanceID || event.Component != "rpc" || event.Message != "消息\n第二行" || event.Timestamp.IsZero() || event.Fields["error"] != "原始错误" || event.Fields["nested"].(map[string]any)["value"] != "原值" {
			t.Fatalf("正文不符: %+v", event)
		}
	}
	if c.current.Reported {
		t.Fatal("日志自动更新了业务状态")
	}
	if err := c.Info("不能编码", map[string]any{"value": make(chan int)}); err == nil {
		t.Fatal("非法字段被接收")
	}
	rejected, failed := c.logs.Counts()
	if rejected != 1 || failed != 0 {
		t.Fatalf("阶段计数不符: %d %d", rejected, failed)
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Info("关闭后", nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("关闭后错误不符: %v", err)
	}
}

func TestSendFailureDropsAndConnectionRecovers(t *testing.T) {
	path := filepath.Join(socketDirectory(t), "log.sock")
	cfg := testConfig(t)
	cfg.SyslogSocketPath = path
	m := testManager(t, cfg)
	c := testComponent(t, m, "rpc")
	if err := c.Error("失败即放弃", nil); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { _, failed := c.logs.Counts(); return failed == 1 })
	receiver := unixReceiver(t, path)
	if err := c.Info("恢复后新日志", nil); err != nil {
		t.Fatal(err)
	}
	if data := receive(t, receiver); !strings.Contains(string(data), "恢复后新日志") || strings.Contains(string(data), "失败即放弃") {
		t.Fatalf("失败日志被补发: %s", data)
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatalf("历史发送失败污染关闭结果: %v", err)
	}
	rejected, failed := c.logs.Counts()
	if rejected != 0 || failed != 1 || c.current.Reported {
		t.Fatal("发送失败计数或状态隔离不符")
	}
}

func TestRepresentativeModuleRates(t *testing.T) {
	for _, test := range []struct{ rate, count int }{{1, 2}, {10, 6}, {100, 21}} {
		t.Run(fmt.Sprint(test.rate), func(t *testing.T) {
			path := filepath.Join(socketDirectory(t), "log.sock")
			receiver := unixReceiver(t, path)
			cfg := testConfig(t)
			cfg.SyslogSocketPath = path
			m := testManager(t, cfg)
			c := testComponent(t, m, "rpc")
			for n := 0; n < test.count; n++ {
				if err := c.Info(strings.Repeat("x", 512), map[string]any{"sequence": n}); err != nil {
					t.Fatal(err)
				}
				data := receive(t, receiver)
				if !strings.Contains(string(data), fmt.Sprintf(`"sequence":%d`, n)) {
					t.Fatal("FIFO 顺序不符")
				}
				if n+1 < test.count {
					time.Sleep(time.Second / time.Duration(test.rate))
				}
			}
			if err := m.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			if rejected, failed := c.logs.Counts(); rejected != 0 || failed != 0 {
				t.Fatal("正常低频负载发生丢弃")
			}
		})
	}
}

func TestModuleBackpressureIsolationAndBoundedClose(t *testing.T) {
	path := filepath.Join(socketDirectory(t), "log.sock")
	_ = unixReceiver(t, path)
	cfg := testConfig(t)
	cfg.SyslogSocketPath = path
	cfg.File.Enabled = true
	m := testManager(t, cfg)
	a, err := m.Register("saturated", 64)
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Register("independent", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.StartFile(); err != nil {
		t.Fatal(err)
	}
	paths, _ := filepath.Glob(filepath.Join(cfg.File.OutputDir, "*.prom"))
	if err := os.Remove(paths[0]); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(cfg.File.OutputDir); err != nil {
		t.Fatal(err)
	}
	full := false
	for n := 0; n < 1000; n++ {
		if err := a.Info(strings.Repeat("x", 32*1024), nil); errors.Is(err, ErrQueueFull) {
			full = true
			break
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if !full {
		t.Fatal("未形成可验证的积压")
	}
	if err := b.Info("独立模块仍可接收", nil); err != nil {
		t.Fatal(err)
	}
	a.Unhealthy()
	if currentRecord(t, m, "saturated").State != 3 {
		t.Fatal("日志积压阻止了同步状态提交")
	}
	if rejected, _ := b.logs.Counts(); rejected != 0 {
		t.Fatal("其他模块的拒收计数被污染")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := m.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("未按首次期限停止排空: %v", err)
	}
	final := m.Close(context.Background())
	var pathErr *os.PathError
	if !errors.Is(final, context.DeadlineExceeded) || !errors.Is(final, os.ErrNotExist) || !errors.As(final, &pathErr) {
		t.Fatalf("重复关闭未保留多个出口的错误身份: %v", final)
	}
}
