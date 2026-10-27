package syslogger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	methods := []func(string, map[string]any){
		c.Emergency,
		c.Alert,
		c.Critical,
		c.Error,
		c.Warning,
		c.Notice,
		c.Info,
		c.Debug,
	}
	names := []string{"emergency", "alert", "critical", "error", "warning", "notice", "info", "debug"}
	for level, method := range methods {
		fields := map[string]any{"error": errors.New("原始错误"), "nested": map[string]any{"value": "原值"}}
		method("消息\n第二行", fields)
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
	c.Info("不能编码", map[string]any{"value": make(chan int)})
	rejected, failed := c.logs.Counts()
	if rejected != 1 || failed != 0 {
		t.Fatalf("阶段计数不符: %d %d", rejected, failed)
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.Info("关闭后", nil)
	if rejected, failed := c.logs.Counts(); rejected != 2 || failed != 0 {
		t.Fatalf("关闭后计数不符: rejected=%d failed=%d", rejected, failed)
	}
}

func TestSendFailureDropsAndConnectionRecovers(t *testing.T) {
	capture := captureLogs(t)
	path := filepath.Join(socketDirectory(t), "log.sock")
	cfg := testConfig(t)
	cfg.SyslogSocketPath = path
	m := testManager(t, cfg)
	c := testComponent(t, m, "rpc")
	c.Error("失败即放弃", nil)
	waitUntil(t, func() bool {
		_, failed := c.logs.Counts()
		return failed == 1 && strings.Contains(capture.String(), `component="rpc"`)
	})
	receiver := unixReceiver(t, path)
	c.Info("恢复后新日志", nil)
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
				c.Info(strings.Repeat("x", 512), map[string]any{"sequence": n})
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
	capture := captureLogs(t)
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
	if err := os.Remove(cfg.File.OutputPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Dir(cfg.File.OutputPath)); err != nil {
		t.Fatal(err)
	}
	full := false
	for n := 0; n < 1000; n++ {
		a.Info(strings.Repeat("x", 32*1024), nil)
		if rejected, _ := a.logs.Counts(); rejected != 0 {
			full = true
			break
		}
	}
	if !full {
		t.Fatal("未形成可验证的积压")
	}
	if !strings.Contains(capture.String(), `component="saturated"`) {
		t.Fatal("队满拒收未通过标准日志诊断")
	}
	b.Info("独立模块仍可接收", nil)
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

func TestLogRejectionDiagnostics(t *testing.T) {
	for _, test := range []struct {
		name       string
		fields     map[string]any
		closed     bool
		wantReason string
	}{
		{
			name: "encoding",
			fields: map[string]any{
				"value": make(chan int),
			},
			wantReason: "chan int",
		},
		{
			name:       "closed",
			closed:     true,
			wantReason: ErrClosed.Error(),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			capture := captureLogs(t)
			cfg := testConfig(t)
			cfg.DegradeOnQueueFull = true
			m := testManager(t, cfg)
			c := testComponent(t, m, "rpc")
			c.Healthy()
			before := currentRecord(t, m, "rpc")
			if test.closed {
				if err := m.Close(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			c.Info("拒收", test.fields)
			if rejected, failed := c.logs.Counts(); rejected != 1 || failed != 0 {
				t.Fatalf("阶段计数不符: rejected=%d failed=%d", rejected, failed)
			}
			if currentRecord(t, m, "rpc") != before {
				t.Fatal("提交失败改变了业务健康状态")
			}
			if !c.lastQueueFull.IsZero() {
				t.Fatal("非队满拒收触发了自动降级窗口")
			}
			text := capture.String()
			for _, want := range []string{
				`project="demo"`,
				`instance_id="instance-a"`,
				`component="rpc"`,
				test.wantReason,
			} {
				if !strings.Contains(text, want) {
					t.Fatalf("标准日志缺少诊断信息 %q: %s", want, text)
				}
			}
		})
	}
}

func TestLogInvalidComponents(t *testing.T) {
	for _, test := range []struct {
		name          string
		component     *Component
		wantComponent string
	}{
		{
			name: "nil",
		},
		{
			name:      "zero",
			component: &Component{},
		},
		{
			name:          "missing_manager",
			component:     &Component{name: "rpc"},
			wantComponent: "rpc",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			capture := captureLogs(t)
			test.component.Healthy()
			test.component.Degraded()
			test.component.Unhealthy()
			message := "空句柄\n详细信息"
			test.component.Error(message, map[string]any{
				"error": errors.New("业务错误"),
			})
			lines := strings.Split(strings.TrimSuffix(capture.String(), "\n"), "\n")
			if len(lines) != 2 || !strings.HasSuffix(lines[0], "syslogger: 无效组件句柄，日志已拒收") {
				t.Fatalf("无效句柄未输出警告及独立 JSON 行: %q", lines)
			}
			var event struct {
				Timestamp  time.Time      `json:"timestamp"`
				Level      string         `json:"level"`
				Project    string         `json:"project"`
				InstanceID string         `json:"instance_id"`
				Component  string         `json:"component"`
				Message    string         `json:"message"`
				Fields     map[string]any `json:"fields"`
			}
			if err := json.Unmarshal([]byte(lines[1]), &event); err != nil {
				t.Fatalf("标准日志中的事件 JSON 无效: %v", err)
			}
			if event.Project != "" || event.InstanceID != "" || event.Component != test.wantComponent {
				t.Fatalf("无管理器事件的身份不符: %+v", event)
			}
			if event.Level != "error" || event.Message != message || event.Timestamp.IsZero() {
				t.Fatalf("原始日志等级、消息或时间未保留: %+v", event)
			}
			if event.Fields["error"] != "业务错误" {
				t.Fatalf("原始日志字段未保留: %+v", event.Fields)
			}
		})
	}
}

func TestInvalidComponentEncodingFailure(t *testing.T) {
	capture := captureLogs(t)
	var component *Component
	component.Info("不能编码", map[string]any{
		"value": make(chan int),
	})
	lines := strings.Split(strings.TrimSuffix(capture.String(), "\n"), "\n")
	if len(lines) != 2 || !strings.HasSuffix(lines[0], "syslogger: 无效组件句柄，日志已拒收") {
		t.Fatalf("编码失败时未保留句柄警告: %q", lines)
	}
	if !strings.Contains(lines[1], "chan int") {
		t.Fatalf("编码失败原因未通过标准日志输出: %s", lines[1])
	}
}

func TestConcurrentInvalidComponentLogs(t *testing.T) {
	capture := captureLogs(t)
	var component *Component
	var workers sync.WaitGroup
	for n := 0; n < 8; n++ {
		workers.Add(1)
		go func(n int) {
			defer workers.Done()
			for i := 0; i < 20; i++ {
				component.Info(fmt.Sprintf("%d-%d", n, i), nil)
			}
		}(n)
	}
	workers.Wait()
	lines := strings.Split(strings.TrimSuffix(capture.String(), "\n"), "\n")
	if len(lines) != 8*20*2 {
		t.Fatalf("并发回退日志的行数不符: %d", len(lines))
	}
	seen := make(map[string]bool)
	for i := 0; i < len(lines); i += 2 {
		if !strings.HasSuffix(lines[i], "syslogger: 无效组件句柄，日志已拒收") {
			t.Fatalf("并发警告与事件发生交错: %s", lines[i])
		}
		var event struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal([]byte(lines[i+1]), &event); err != nil {
			t.Fatalf("并发事件 JSON 无效: %v", err)
		}
		if event.Message == "" || seen[event.Message] {
			t.Fatalf("并发事件缺失消息或发生重复: %+v", event)
		}
		seen[event.Message] = true
	}
}

func TestConcurrentRejectedLogs(t *testing.T) {
	capture := captureLogs(t)
	m := testManager(t, testConfig(t))
	c := testComponent(t, m, "rpc")
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for n := 0; n < 8; n++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := 0; i < 100; i++ {
				c.Info("关闭后的并发提交", nil)
			}
		}()
	}
	workers.Wait()
	if rejected, failed := c.logs.Counts(); rejected != 800 || failed != 0 {
		t.Fatalf("并发拒收计数不符: rejected=%d failed=%d", rejected, failed)
	}
	if !strings.Contains(capture.String(), `component="rpc"`) {
		t.Fatal("并发拒收未通过标准日志诊断")
	}
}
