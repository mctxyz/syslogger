package syslogger

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestConcurrentStateSnapshotsAndClose(t *testing.T) {
	m := testManager(t, testConfig(t))
	a := testComponent(t, m, "a")
	b := testComponent(t, m, "b")
	var workers sync.WaitGroup
	for n := 0; n < 8; n++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := 0; i < 500; i++ {
				a.Healthy()
				b.Unhealthy()
				_ = m.snapshot()
			}
		}()
	}
	workers.Wait()
	a.Degraded()
	b.Healthy()
	beforeA, beforeB := currentRecord(t, m, "a"), currentRecord(t, m, "b")
	for n := 0; n < 8; n++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if err := m.Close(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	workers.Wait()
	a.Healthy()
	b.Unhealthy()
	if currentRecord(t, m, "a") != beforeA || currentRecord(t, m, "b") != beforeB {
		t.Fatal("关闭截止破坏了完整记录")
	}
}

func TestConcurrentOutletsRegisterAndClose(t *testing.T) {
	cfg := testConfig(t)
	cfg.HTTP = HTTPConfig{Enabled: true, ListenAddr: "127.0.0.1:0"}
	cfg.File.Enabled = true
	m := testManager(t, cfg)
	existing := testComponent(t, m, "existing")
	var workers sync.WaitGroup
	for n := 0; n < 24; n++ {
		workers.Add(1)
		go func(n int) {
			defer workers.Done()
			switch n % 4 {
			case 0:
				_, _ = m.Register(fmt.Sprint(n), 1)
			case 1:
				_ = m.StartHTTP()
			case 2:
				_ = m.StartFile()
			case 3:
				_ = m.Close(context.Background())
			}
		}(n)
	}
	workers.Wait()
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.components["existing"] != existing {
		t.Fatal("并发生命周期改变既有句柄")
	}
}

func TestConcurrentQueueHealthAndClose(t *testing.T) {
	captureLogs(t)
	path := filepath.Join(socketDirectory(t), "concurrent-queue-health.sock")
	_ = unixReceiver(t, path)
	cfg := testConfig(t)
	cfg.SyslogSocketPath = path
	cfg.DegradeOnQueueFull = true
	cfg.QueueFullRecoveryInterval = time.Millisecond
	m := testManager(t, cfg)
	c, err := m.Register("rpc", 1)
	if err != nil {
		t.Fatal(err)
	}
	saturateLogQueue(t, c)
	var workers sync.WaitGroup
	start := make(chan struct{})
	for n := 0; n < 4; n++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			for i := 0; i < 100; i++ {
				c.Info("并发提交", nil)
				c.Unhealthy()
				c.Healthy()
				if _, err := m.Registry().Gather(); err != nil {
					t.Errorf("并发采集失败: %v", err)
					return
				}
			}
		}()
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		<-start
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := m.Close(ctx); err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("并发关闭返回意外错误: %v", err)
		}
	}()
	close(start)
	workers.Wait()
	if err := m.Close(context.Background()); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	frozen := currentRecord(t, m, "rpc")
	c.Healthy()
	c.mu.RLock()
	later := c.healthAt(time.Now().Add(time.Hour))
	c.mu.RUnlock()
	if later != frozen || currentRecord(t, m, "rpc") != frozen {
		t.Fatal("并发关闭后健康记录仍发生变化")
	}
}
