package syslogger

import (
	"context"
	"fmt"
	"sync"
	"testing"
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
