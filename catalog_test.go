package syslogger

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/mctxyz/syslogger/internal/catalog"
	"github.com/mctxyz/syslogger/internal/diagnostic"
)

// catalogManager 在隔离目录中验证真实发布逻辑，固定系统路径留给平台上的运行验证。
func catalogManager(t *testing.T, cfg Config, root string) *Manager {
	t.Helper()
	cfg.CatalogEnabled = true
	normalized, err := cfg.normalized()
	if err != nil {
		t.Fatal(err)
	}
	m := newManager(normalized)
	if m.catalog == nil || m.catalog.Root != catalog.Root {
		t.Fatal("目录开关没有建立固定系统目录的存储")
	}
	m.catalog.Root = root
	m.registry.MustRegister(m)
	m.publishCatalog()
	t.Cleanup(func() {
		_ = m.Close(context.Background())
	})
	return m
}

func TestCatalogDefaultAndDescriptions(t *testing.T) {
	cfg := testConfig(t)
	cfg.InstanceDescription = "主网监听"
	m := testManager(t, cfg)
	if m.catalog != nil || m.cfg.CatalogEnabled {
		t.Fatal("默认配置启用了目录发布")
	}
	c, err := m.Register(ComponentConfig{Name: "rpc", Description: "日志获取"})
	if err != nil || c.description != "日志获取" || m.cfg.InstanceDescription != "主网监听" {
		t.Fatalf("描述没有保存在实际实例和组件上: %v", err)
	}
	safe := testSafeManager(t, cfg)
	if safe.catalog != nil || safe.cfg.InstanceDescription != cfg.InstanceDescription {
		t.Fatal("SafeNew 没有保留目录默认值或实例描述")
	}
}

func TestCatalogRegistrationRestartAndClose(t *testing.T) {
	cfg := testConfig(t)
	cfg.StateEnabled = false
	cfg.InstanceDescription = "旧实例说明"
	root := filepath.Join(t.TempDir(), "catalog")
	first := catalogManager(t, cfg, root)
	if _, err := first.Register(ComponentConfig{Name: "obsolete", Description: "旧组件"}); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	instances, err := first.catalog.Read()
	if err != nil || len(instances) != 1 || len(instances[0].Components) != 1 {
		t.Fatalf("关闭后目录未保留: %+v, %v", instances, err)
	}
	cfg.InstanceDescription = "新实例说明"
	second := catalogManager(t, cfg, root)
	instances, err = second.catalog.Read()
	if err != nil || len(instances) != 1 || len(instances[0].Components) != 0 {
		t.Fatalf("重新初始化未覆盖旧组件: %+v, %v", instances, err)
	}
	if _, err := second.Register(ComponentConfig{Name: "rpc", Description: "新区块获取"}); err != nil {
		t.Fatal(err)
	}
	instances, err = second.catalog.Read()
	want := []catalog.Instance{{Project: cfg.Project, InstanceID: cfg.InstanceID, InstanceDescription: cfg.InstanceDescription, Components: []catalog.Component{{Name: "rpc", Description: "新区块获取"}}}}
	if err != nil || !reflect.DeepEqual(instances, want) {
		t.Fatalf("注册后目录内容不符: got=%+v err=%v", instances, err)
	}
}

func TestConcurrentCatalogRegistration(t *testing.T) {
	cfg := testConfig(t)
	m := catalogManager(t, cfg, t.TempDir())
	var workers sync.WaitGroup
	for i := 0; i < 12; i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			if _, err := m.Register(ComponentConfig{Name: fmt.Sprintf("component-%02d", i), Description: "并发注册"}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	workers.Wait()
	instances, err := m.catalog.Read()
	if err != nil || len(instances) != 1 || len(instances[0].Components) != 13 {
		t.Fatalf("并发发布遗漏组件: %+v, %v", instances, err)
	}
	if instances[0].Components[12].Name != diagnostic.ComponentName {
		t.Fatal("目录缺少管理器诊断组件")
	}
}

func TestCatalogFailurePreservesBusinessRegistration(t *testing.T) {
	capture := captureLogs(t)
	cfg := testConfig(t)
	root := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(root, []byte("保留"), 0600); err != nil {
		t.Fatal(err)
	}
	m := catalogManager(t, cfg, root)
	c, err := m.Register(ComponentConfig{Name: "rpc", Description: "日志获取"})
	if err != nil || c == nil || c.logs == nil {
		t.Fatalf("辅助目录失败阻断了业务注册: %v", err)
	}
	c.Healthy()
	if currentRecord(t, m, "rpc").State != int(StateHealthy) || currentRecord(t, m, diagnostic.ComponentName).State != int(StateHealthy) {
		t.Fatal("辅助目录失败改变了原有健康状态")
	}
	if !strings.Contains(capture.String(), "发布日志名称目录失败") {
		t.Fatal("辅助目录失败未诊断")
	}
}
