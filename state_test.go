package syslogger

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/mctxyz/syslogger/internal/record"
)

func TestQueueHealthStateComposition(t *testing.T) {
	cfg := testConfig(t)
	cfg.DegradeOnQueueFull = true
	m := testManager(t, cfg)
	c := testComponent(t, m, "rpc")
	droppedAt := time.Unix(100, 0)
	deadline := droppedAt.Add(30 * time.Second)
	healthy := record.New(int(StateHealthy), droppedAt.Add(-time.Second))
	for _, test := range []struct {
		name     string
		business record.Record
		lastDrop time.Time
		at       time.Time
		want     record.Record
	}{
		{
			name: "initial_unknown",
			at:   droppedAt,
		},
		{
			name:     "first_drop",
			lastDrop: droppedAt,
			at:       droppedAt,
			want:     record.New(int(StateDegraded), droppedAt),
		},
		{
			name:     "healthy_with_active_window",
			business: healthy,
			lastDrop: droppedAt,
			at:       deadline.Add(-time.Nanosecond),
			want:     record.New(int(StateDegraded), droppedAt),
		},
		{
			name:     "recovery_at_deadline",
			lastDrop: droppedAt,
			at:       deadline,
			want:     record.New(int(StateHealthy), deadline),
		},
		{
			name:     "repeated_drop_resets_window",
			business: healthy,
			lastDrop: droppedAt.Add(time.Second),
			at:       deadline,
			want:     record.New(int(StateDegraded), droppedAt.Add(time.Second)),
		},
		{
			name:     "recent_business_healthy",
			business: record.New(int(StateHealthy), deadline.Add(time.Second)),
			lastDrop: droppedAt,
			at:       deadline.Add(2 * time.Second),
			want:     record.New(int(StateHealthy), deadline.Add(time.Second)),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			c.mu.Lock()
			c.current = test.business
			c.lastQueueFull = test.lastDrop
			got := c.healthAt(test.at)
			business := c.current
			c.mu.Unlock()
			if got != test.want || business != test.business {
				t.Fatalf("状态合成不符或覆盖了业务记录: got=%+v want=%+v business=%+v", got, test.want, business)
			}
		})
	}
	for _, state := range []State{StateDegraded, StateUnhealthy} {
		business := record.New(int(state), droppedAt.Add(-time.Second))
		c.mu.Lock()
		c.current = business
		c.lastQueueFull = droppedAt
		during := c.healthAt(droppedAt)
		after := c.healthAt(deadline)
		c.mu.Unlock()
		if during != business || after != business {
			t.Fatalf("队满或窗口恢复覆盖了业务主动故障: state=%d during=%+v after=%+v", state, during, after)
		}
	}
}

func TestQueueHealthWindowOutletsAndClose(t *testing.T) {
	captureLogs(t)
	path := filepath.Join(socketDirectory(t), "queue-health.sock")
	_ = unixReceiver(t, path)
	cfg := testConfig(t)
	cfg.SyslogSocketPath = path
	cfg.DegradeOnQueueFull = true
	cfg.QueueFullRecoveryInterval = 500 * time.Millisecond
	cfg.HTTP.Enabled = true
	cfg.File.Enabled = true
	cfg.File.FlushInterval = 10 * time.Millisecond
	m := testManager(t, cfg)
	// 背压用例以取消上下文收尾，避免等待未读取的 socket 排空。
	t.Cleanup(func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_ = m.Close(ctx)
		_ = m.Close(context.Background())
	})
	a, err := m.Register(ComponentConfig{Name: "saturated", QueueCapacity: 64})
	if err != nil {
		t.Fatal(err)
	}
	b := testComponent(t, m, "independent")
	a.Healthy()
	b.Healthy()
	firstDrop := saturateLogQueue(t, a)
	if firstDrop.IsZero() || currentRecord(t, m, "saturated").State != int(StateDegraded) {
		t.Fatal("真实队满未产生自动降级")
	}
	lastDrop := saturateLogQueue(t, a)
	a.mu.RLock()
	atOriginalDeadline := a.healthAt(firstDrop.Add(cfg.QueueFullRecoveryInterval))
	a.mu.RUnlock()
	if !lastDrop.After(firstDrop) || atOriginalDeadline.State != int(StateDegraded) {
		t.Fatal("再次队满未重置恢复窗口")
	}
	if currentRecord(t, m, "independent").State != int(StateHealthy) {
		t.Fatal("一个组件的队满影响了其他组件")
	}
	handler, err := m.Handler()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.StartFile(); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	labels := map[string]string{
		"project":     cfg.Project,
		"instance_id": cfg.InstanceID,
		"component":   "saturated",
	}
	for _, text := range []string{response.Body.String(), string(readSnapshot(t, cfg.File.OutputPath))} {
		if !hasMetricSample(t, text, "syslogger_component_state", labels, 2) {
			t.Fatalf("指标出口未展示自动降级: %s", text)
		}
	}
	rejectedBefore, _ := a.logs.Counts()
	// socket 持续停止读取，恢复由无丢弃窗口决定，并由已有周期快照自然显示。
	waitUntil(t, func() bool {
		state := currentRecord(t, m, "saturated").State
		file := string(readSnapshot(t, cfg.File.OutputPath))
		return state == int(StateHealthy) && hasMetricSample(t, file, "syslogger_component_state", labels, 1)
	})
	if rejected, _ := a.logs.Counts(); rejected != rejectedBefore {
		t.Fatal("自动恢复改变了累计拒收次数")
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	if !hasMetricSample(t, response.Body.String(), "syslogger_component_state", labels, 1) {
		t.Fatal("HTTP 快照未按无丢弃窗口恢复")
	}
	a.Unhealthy()
	business := currentRecord(t, m, "saturated")
	saturateLogQueue(t, a)
	if currentRecord(t, m, "saturated") != business {
		t.Fatal("队满自动状态覆盖了业务主动 Unhealthy")
	}
	a.Healthy()
	lastDrop = saturateLogQueue(t, a)
	frozen := currentRecord(t, m, "saturated")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = m.Close(ctx)
	if err := m.Close(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("背压关闭未保留取消原因: %v", err)
	}
	finalFile := readSnapshot(t, cfg.File.OutputPath)
	if frozen.State != int(StateDegraded) || !hasMetricSample(t, string(finalFile), "syslogger_component_state", labels, 2) {
		t.Fatal("最终文件未保留截止时的自动降级")
	}
	time.Sleep(time.Until(lastDrop.Add(cfg.QueueFullRecoveryInterval)) + time.Millisecond)
	a.Healthy()
	if currentRecord(t, m, "saturated") != frozen {
		t.Fatal("关闭后自动恢复或业务上报改变了截止记录")
	}
	if !bytes.Equal(finalFile, readSnapshot(t, cfg.File.OutputPath)) {
		t.Fatal("关闭后的状态变化改写了最终文件")
	}
}

func TestQueueHealthDisabled(t *testing.T) {
	captureLogs(t)
	path := filepath.Join(socketDirectory(t), "queue-health-disabled.sock")
	_ = unixReceiver(t, path)
	cfg := testConfig(t)
	cfg.SyslogSocketPath = path
	m := testManager(t, cfg)
	t.Cleanup(func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_ = m.Close(ctx)
		_ = m.Close(context.Background())
	})
	c := testComponent(t, m, "rpc")
	c.Healthy()
	before := currentRecord(t, m, "rpc")
	if droppedAt := saturateLogQueue(t, c); !droppedAt.IsZero() {
		t.Fatal("功能关闭时仍记录了自动恢复窗口")
	}
	if currentRecord(t, m, "rpc") != before {
		t.Fatal("功能关闭时队满改变了业务状态")
	}
}
