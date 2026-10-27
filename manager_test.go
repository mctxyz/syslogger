package syslogger

import (
	"context"
	"fmt"
	"testing"

	"github.com/mctxyz/syslogger/internal/diagnostic"
)

func TestRegistrationCapacityAndPreservation(t *testing.T) {
	m := testManager(t, testConfig(t))
	initial := currentRecord(t, m, diagnostic.ComponentName)
	for _, capacity := range []int{-1, 0, 1, 4095, 4096, 4097} {
		c, err := m.Register(fmt.Sprint(capacity), capacity)
		if err != nil || c.logs == nil {
			t.Fatalf("容量 %d 未成功注册: %v", capacity, err)
		}
	}
	for _, name := range []string{"RPC", " rpc ", "中文 组件!", "syslogger_manager ", "SYSLOGGER_MANAGER"} {
		if c := testComponent(t, m, name); c.name != name {
			t.Fatal("组件身份原文被改变")
		}
	}
	c := testComponent(t, m, "rpc")
	c.Unhealthy()
	fact := currentRecord(t, m, "rpc")
	if currentRecord(t, m, diagnostic.ComponentName) != initial {
		t.Fatal("普通成功注册改变了初始化事实")
	}
	for _, name := range []string{"", " \t", "bad\xff", diagnostic.ComponentName, "rpc"} {
		if next, err := m.Register(name, 1); next != nil || err == nil {
			t.Fatalf("非法名称未拒绝: %q", name)
		}
		if m.components["rpc"] != c || currentRecord(t, m, "rpc") != fact {
			t.Fatal("失败注册破坏了已有组件")
		}
	}
	if currentRecord(t, m, diagnostic.ComponentName).State != int(StateDegraded) {
		t.Fatal("注册失败未记录管理器降级")
	}
	testComponent(t, m, "新组件")
	if currentRecord(t, m, diagnostic.ComponentName).State != int(StateHealthy) {
		t.Fatal("注册成功未恢复管理器")
	}
}

func TestStateSwitchAndClose(t *testing.T) {
	cfg := testConfig(t)
	m := testManager(t, cfg)
	c := testComponent(t, m, "rpc")
	if got := currentRecord(t, m, "rpc"); got.Reported || got.State != 0 {
		t.Fatal("初始状态不符")
	}
	c.Healthy()
	c.Degraded()
	c.Unhealthy()
	got := currentRecord(t, m, "rpc")
	if !got.Reported || got.State != 3 || got.ObservedAt.IsZero() {
		t.Fatalf("同步状态不符: %+v", got)
	}
	c.Unhealthy()
	if currentRecord(t, m, "rpc").ObservedAt.Before(got.ObservedAt) {
		t.Fatal("相同状态未刷新时间")
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	got = currentRecord(t, m, "rpc")
	c.Healthy()
	if currentRecord(t, m, "rpc") != got {
		t.Fatal("关闭后状态改变")
	}
	cfg.StateEnabled = false
	off := testManager(t, cfg)
	disabled := testComponent(t, off, "rpc")
	disabled.Healthy()
	disabled.Degraded()
	disabled.Unhealthy()
	if disabled.current.Reported || off.diagnostic != nil {
		t.Fatal("状态关闭仍执行更新")
	}
	var nilComponent *Component
	nilComponent.Healthy()
	nilComponent.Degraded()
	nilComponent.Unhealthy()
}

func TestRegistrationPanicIsolation(t *testing.T) {
	m := testManager(t, testConfig(t))
	c := testComponent(t, m, "rpc")
	tracker := m.diagnostic
	m.diagnostic = nil
	component, err := m.Register("new", 1)
	m.diagnostic = tracker
	if component != nil || err == nil || m.components["new"] != nil || m.components["rpc"] != c {
		t.Fatal("注册 panic 未回收本次部分状态")
	}
	components := m.components
	m.components = nil
	component, err = m.Register("map-panic", 1)
	m.components = components
	if component != nil || err == nil {
		t.Fatal("注册存储 panic 未转换为错误")
	}
	testComponent(t, m, "after-panic")
}
