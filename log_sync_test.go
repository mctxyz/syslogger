package syslogger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestEightSynchronousLevels(t *testing.T) {
	path := filepath.Join(socketDirectory(t), "sync.sock")
	receiver := unixReceiver(t, path)
	cfg := testConfig(t)
	cfg.SyslogSocketPath = path
	m := testManager(t, cfg)
	c := testComponent(t, m, "rpc")
	methods := []func(context.Context, string, Fields) error{
		c.EmergencySync,
		c.AlertSync,
		c.CriticalSync,
		c.ErrorSync,
		c.WarningSync,
		c.NoticeSync,
		c.InfoSync,
		c.DebugSync,
	}
	names := []string{"emergency", "alert", "critical", "error", "warning", "notice", "info", "debug"}
	for level, method := range methods {
		fields := Fields{"large": ^uint64(0), "error": errors.New("详情")}
		if err := method(context.Background(), "同步消息\n第二行", fields); err != nil {
			t.Fatal(err)
		}
		fields["large"] = 0
		data := receive(t, receiver)
		if !strings.HasPrefix(string(data), fmt.Sprintf("<%d>", 8+level)) || strings.Contains(string(data), "\n") {
			t.Fatalf("同步日志等级或单行封装错误: %s", data)
		}
		var body struct {
			Timestamp  string `json:"timestamp"`
			Level      string `json:"level"`
			Project    string `json:"project"`
			InstanceID string `json:"instance_id"`
			Component  string `json:"component"`
			Message    string `json:"message"`
			Fields     struct {
				Large uint64 `json:"large"`
				Error string `json:"error"`
			} `json:"fields"`
		}
		start := strings.IndexByte(string(data), '{')
		if start < 0 || json.Unmarshal(data[start:], &body) != nil {
			t.Fatalf("同步正文不是完整 JSON: %s", data)
		}
		if body.Timestamp == "" || body.Level != names[level] {
			t.Fatalf("同步时间或等级错误: %+v", body)
		}
		if body.Project != cfg.Project || body.InstanceID != cfg.InstanceID || body.Component != "rpc" {
			t.Fatalf("同步身份错误: %+v", body)
		}
		if body.Message != "同步消息\n第二行" || body.Fields.Large != ^uint64(0) || body.Fields.Error != "详情" {
			t.Fatalf("同步消息或字段快照错误: %+v", body)
		}
	}
	if rejected, failed := c.logs.Counts(); rejected != 0 || failed != 0 || len(c.health) != 0 {
		t.Fatal("同步发送改变了健康状态或错误计数")
	}
}

func TestSynchronousLogErrors(t *testing.T) {
	capture := captureLogs(t)
	m := testManager(t, testConfig(t))
	c := testComponent(t, m, "rpc")
	if err := c.InfoSync(nil, "nil context", nil); err == nil {
		t.Fatal("nil 上下文没有被拒绝")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.InfoSync(canceled, "取消", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("预先取消未保留原因: %v", err)
	}
	if err := c.InfoSync(context.Background(), "编码失败", Fields{"value": make(chan int)}); err == nil {
		t.Fatal("同步日志忽略了编码失败")
	}
	if err := c.InfoSync(context.Background(), "连接失败", nil); err == nil {
		t.Fatal("同步日志忽略了连接失败")
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatalf("关闭复用了运行期间的同步错误: %v", err)
	}
	if err := c.InfoSync(context.Background(), "关闭后", nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("同步调用没有遵守接收截止: %v", err)
	}
	if rejected, failed := c.logs.Counts(); rejected != 4 || failed != 1 {
		t.Fatalf("同步接收与发送计数混淆: rejected=%d failed=%d", rejected, failed)
	}
	for _, invalid := range []*Component{nil, {}} {
		if err := invalid.ErrorSync(context.Background(), "无效句柄", nil); err == nil {
			t.Fatal("无效句柄未返回错误")
		}
	}
	if capture.String() != "" {
		t.Fatalf("同步调用重复输出了返回的错误: %s", capture.String())
	}
}

func TestConcurrentSynchronousLogs(t *testing.T) {
	path := filepath.Join(socketDirectory(t), "sync.sock")
	receiver := unixReceiver(t, path)
	cfg := testConfig(t)
	cfg.SyslogSocketPath = path
	m := testManager(t, cfg)
	c := testComponent(t, m, "rpc")
	const count = 32
	var callers sync.WaitGroup
	for index := 0; index < count; index++ {
		callers.Add(1)
		go func(index int) {
			defer callers.Done()
			if err := c.InfoSync(context.Background(), "并发", Fields{"sequence": index}); err != nil {
				t.Error(err)
			}
		}(index)
	}
	seen := make(map[int]bool)
	for index := 0; index < count; index++ {
		data := receive(t, receiver)
		start := strings.IndexByte(string(data), '{')
		var body struct {
			Fields struct {
				Sequence int `json:"sequence"`
			} `json:"fields"`
		}
		if start < 0 || json.Unmarshal(data[start:], &body) != nil || seen[body.Fields.Sequence] {
			t.Fatalf("并发同步正文丢失、重复或交错: %s", data)
		}
		seen[body.Fields.Sequence] = true
	}
	callers.Wait()
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rejected, failed := c.logs.Counts(); rejected != 0 || failed != 0 {
		t.Fatal("并发同步交付被误计为失败")
	}
}

func TestSynchronousCatalogActivity(t *testing.T) {
	cfg := testConfig(t)
	m := catalogManager(t, cfg, preparedCatalogRoot(t))
	c := testComponent(t, m, "rpc")
	if err := c.InfoSync(context.Background(), "已接收但连接失败", nil); err == nil {
		t.Fatal("测试缺省 socket 意外可用")
	}
	if err := c.InfoSync(nil, "未接收", nil); err == nil {
		t.Fatal("nil 上下文未被拒绝")
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	instances, err := m.catalog.Read()
	if err != nil || len(instances) != 1 || len(instances[0].Components) != 1 || catalogLogCount(instances[0].Components[0]) != 1 {
		t.Fatalf("同步接收量没有进入最终目录，或拒收被计入: %+v, %v", instances, err)
	}
	if component := instances[0].Components[0]; component.CreatedAt.IsZero() || component.LastActiveAt.Before(component.CreatedAt) {
		t.Fatalf("同步活动时间不完整: %+v", component)
	}
	if _, failed := c.logs.Counts(); failed != 1 {
		t.Fatal("同步发送失败没有进入现有指标来源")
	}
}
