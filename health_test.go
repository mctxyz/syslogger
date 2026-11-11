package syslogger

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mctxyz/syslogger/internal/record"
)

func TestNamedHealthReports(t *testing.T) {
	m := testManager(t, testConfig(t))
	a := testComponent(t, m, "a")
	b := testComponent(t, m, "b")
	if got := currentRecord(t, m, "a"); got != (record.Record{}) || len(a.health) != 0 {
		t.Fatalf("没有报告时应为 Unknown/0: %+v", got)
	}
	a.Healthy("connection")
	first := a.health["connection"]
	if currentRecord(t, m, "a") != first || first.State != int(StateHealthy) {
		t.Fatal("首份业务报告未直接建立整体健康")
	}
	b.Unhealthy("connection")
	a.Unhealthy("storage")
	if currentRecord(t, m, "a").State != int(StateDegraded) || currentRecord(t, m, "b").State != int(StateUnhealthy) {
		t.Fatal("混合业务状态未降级，或跨组件同名状态发生串扰")
	}
	storage := a.health["storage"]
	if a.health["connection"] != first {
		t.Fatal("其他名称的报告覆盖了既有记录")
	}
	a.Healthy("connection")
	if len(a.health) != 2 || a.health["connection"].ObservedAt.Before(first.ObservedAt) || a.health["storage"] != storage {
		t.Fatal("同名报告未更新原记录，或覆盖了其他名称")
	}
	a.Healthy("storage")
	if currentRecord(t, m, "a").State != int(StateHealthy) || currentRecord(t, m, "b").State != int(StateUnhealthy) {
		t.Fatal("组件整体未随独立业务恢复，或影响其他组件")
	}
	before := m.snapshot()
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	a.Unhealthy("late")
	if len(a.health) != 2 {
		t.Fatal("关闭截止后仍建立了新名称")
	}
	for _, item := range before {
		if item.Name == "a" && item.Health[overallHealthName] != currentRecord(t, m, "a") {
			t.Fatal("关闭后整体记录改变")
		}
	}
}

func TestNamedHealthAggregation(t *testing.T) {
	m := testManager(t, testConfig(t))
	c := testComponent(t, m, "rpc")
	at := time.Unix(100, 0)
	for _, test := range []struct {
		first, second, want State
	}{
		{StateHealthy, StateHealthy, StateHealthy},
		{StateHealthy, StateDegraded, StateDegraded},
		{StateHealthy, StateUnhealthy, StateDegraded},
		{StateDegraded, StateHealthy, StateDegraded},
		{StateDegraded, StateDegraded, StateDegraded},
		{StateDegraded, StateUnhealthy, StateDegraded},
		{StateUnhealthy, StateHealthy, StateDegraded},
		{StateUnhealthy, StateDegraded, StateDegraded},
		{StateUnhealthy, StateUnhealthy, StateUnhealthy},
	} {
		t.Run(fmt.Sprintf("%d/%d", test.first, test.second), func(t *testing.T) {
			c.health = map[HealthName]record.Record{
				"first":  record.New(int(test.first), at),
				"second": record.New(int(test.second), at.Add(time.Second)),
			}
			if got := currentRecord(t, m, "rpc"); got != record.New(int(test.want), at.Add(time.Second)) {
				t.Fatalf("业务整体状态或最新汇报时间不符: %+v", got)
			}
		})
	}
}

func TestHealthNameValidation(t *testing.T) {
	for _, name := range []HealthName{"", " \t", "bad\xff", overallHealthName} {
		t.Run(strconv.Quote(string(name)), func(t *testing.T) {
			capture := captureLogs(t)
			m := testManager(t, testConfig(t))
			c := testComponent(t, m, "rpc")
			c.Healthy("existing")
			before := currentRecord(t, m, c.name)
			for _, report := range []func(HealthName){c.Healthy, c.Degraded, c.Unhealthy} {
				report(name)
			}
			if len(c.health) != 1 || c.health["existing"] != before || currentRecord(t, m, c.name) != before {
				t.Fatal("非法报告改变了已有记录或整体状态")
			}
			if rejected, failed := c.logs.Counts(); rejected != 0 || failed != 0 {
				t.Fatal("非法健康报告影响了日志 Counter")
			}
			if !strings.Contains(capture.String(), "name="+strconv.Quote(string(name))) {
				t.Fatalf("非法健康名称缺少独立诊断: %s", capture.String())
			}
		})
	}
	m := testManager(t, testConfig(t))
	c := testComponent(t, m, "rpc")
	for _, name := range []HealthName{"storage", "Storage", " storage ", "连接\"\\\n状态"} {
		c.Healthy(name)
		if _, exists := c.health[name]; !exists {
			t.Fatalf("业务名称未保留原文: %q", name)
		}
	}
	if len(c.health) != 4 {
		t.Fatal("不同原文名称被合并")
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := currentRecord(t, m, c.name)
	c.Healthy(overallHealthName)
	if len(c.health) != 4 || currentRecord(t, m, c.name) != before {
		t.Fatal("关闭后的非法报告改变了记录")
	}
}

func TestHealthDiagnosticsAreIndependent(t *testing.T) {
	capture := captureLogs(t)
	m := testManager(t, testConfig(t))
	c := testComponent(t, m, "rpc")
	c.Healthy(overallHealthName)
	c.Info("编码失败", Fields{"value": make(chan int)})
	text := capture.String()
	if !strings.Contains(text, "健康报告已拒绝") || !strings.Contains(text, "日志提交失败") {
		t.Fatalf("健康诊断抑制了独立日志诊断: %s", text)
	}
	for _, component := range []*Component{nil, {}} {
		component.Unhealthy(overallHealthName)
	}
}

func TestConcurrentNamedHealthSnapshots(t *testing.T) {
	m := testManager(t, testConfig(t))
	c := testComponent(t, m, "runtime")
	var workers sync.WaitGroup
	for n := 0; n < 8; n++ {
		workers.Add(1)
		go func(n int) {
			defer workers.Done()
			name := HealthName(fmt.Sprintf("part-%d", n))
			for iteration := 0; iteration < 100; iteration++ {
				c.Healthy(name)
				c.Unhealthy(name)
				for _, item := range m.snapshot() {
					if item.Name != c.name {
						continue
					}
					var latest time.Time
					for key, business := range item.Health {
						if key != overallHealthName && business.ObservedAt.After(latest) {
							latest = business.ObservedAt
						}
					}
					if !item.Health[overallHealthName].ObservedAt.Equal(latest) {
						t.Error("同一组件的明细和整体不是同次完整快照")
						return
					}
				}
			}
		}(n)
	}
	workers.Wait()
	if len(c.health) != 8 || currentRecord(t, m, c.name).State != int(StateUnhealthy) {
		t.Fatal("并发首次报告丢失名称，或全部不健康未正确汇总")
	}
}
