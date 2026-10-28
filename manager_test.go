package syslogger

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/mctxyz/syslogger/internal/diagnostic"
)

func TestRegistrationCapacityAndPreservation(t *testing.T) {
	m := testManager(t, testConfig(t))
	initial := currentRecord(t, m, diagnostic.ComponentName)
	for _, capacity := range []int{-1, 0, 1, 4095, 4096, 4097} {
		c, err := m.Register(ComponentConfig{Name: fmt.Sprint(capacity), QueueCapacity: capacity})
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
		if next, err := m.Register(ComponentConfig{Name: name, QueueCapacity: 1}); next != nil || err == nil {
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

func TestConfiguredDefaultQueueCapacity(t *testing.T) {
	for _, safe := range []bool{false, true} {
		for _, configured := range []int{-1, 0, 1, 1024, 4096, 4097} {
			t.Run(fmt.Sprintf("safe_%v/default_%d", safe, configured), func(t *testing.T) {
				capture := captureLogs(t)
				cfg := testConfig(t)
				cfg.DefaultQueueCapacity = configured
				invalid := configured < 0 || configured > 4096
				if invalid && !safe {
					if manager, err := New(cfg); manager != nil || err == nil {
						t.Fatalf("严格构造未拒绝非法默认容量: %d", configured)
					}
					return
				}
				var manager *Manager
				if safe {
					manager = testSafeManager(t, cfg)
				} else {
					manager = testManager(t, cfg)
				}
				wantDefault := configured
				if configured == 0 || invalid {
					wantDefault = 512
				}
				if manager.cfg.DefaultQueueCapacity != wantDefault || cfg.DefaultQueueCapacity != configured {
					t.Fatal("默认容量归一化不符或修改了调用方配置")
				}
				for _, requested := range []int{-1, 0, 1, 4095, 4096, 4097} {
					component, err := manager.Register(ComponentConfig{Name: fmt.Sprint(requested), QueueCapacity: requested})
					if err != nil {
						t.Fatal(err)
					}
					want := requested
					if requested < 1 || requested > 4096 {
						want = wantDefault
					}
					// 直接核对实际 channel 容量，生产接口保持现有职责。
					queue := reflect.ValueOf(component.logs).Elem()
					entries := queue.FieldByName("entries")
					if got := entries.Cap(); got != want {
						t.Fatalf("模块容量不符: requested=%d got=%d want=%d", requested, got, want)
					}
				}
				if invalid {
					if currentRecord(t, manager, diagnostic.ComponentName).State != int(StateUnhealthy) {
						t.Fatal("非法默认容量的初始化异常被组件注册覆盖")
					}
					if !strings.Contains(capture.String(), `field="DefaultQueueCapacity"`) {
						t.Fatal("非法默认容量未通过标准日志诊断")
					}
				}
			})
		}
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
	component, err := m.Register(ComponentConfig{Name: "new", QueueCapacity: 1})
	m.diagnostic = tracker
	if component != nil || err == nil || m.components["new"] != nil || m.components["rpc"] != c {
		t.Fatal("注册 panic 未回收本次部分状态")
	}
	components := m.components
	m.components = nil
	component, err = m.Register(ComponentConfig{Name: "map-panic", QueueCapacity: 1})
	m.components = components
	if component != nil || err == nil {
		t.Fatal("注册存储 panic 未转换为错误")
	}
	testComponent(t, m, "after-panic")
}
