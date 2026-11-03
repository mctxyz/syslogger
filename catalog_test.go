package syslogger

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/mctxyz/syslogger/internal/catalog"
	"github.com/mctxyz/syslogger/internal/diagnostic"
	"github.com/mctxyz/syslogger/internal/logging"
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
	m.startCatalog()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = m.Close(ctx)
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
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("初始化或注册提前创建了名称目录: %v", err)
	}
	first.components["obsolete"].Info("首次使用", nil)
	waitUntil(t, func() bool {
		instances, err := first.catalog.Read()
		return err == nil && len(instances) == 1 && instances[0].Components[0].LogCount == 1
	})
	first.components["obsolete"].Info("第二条", nil)
	first.components["obsolete"].Info("第三条", nil)
	// 独立缺省 socket 的发送结果不影响已经成功入队的数量。
	_ = first.Close(context.Background())
	instances, err := first.catalog.Read()
	if err != nil || len(instances) != 1 || len(instances[0].Components) != 1 || instances[0].Components[0].LogCount != 3 {
		t.Fatalf("关闭后目录未保留: %+v, %v", instances, err)
	}
	createdAt := instances[0].CreatedAt
	cfg.InstanceDescription = "新实例说明"
	second := catalogManager(t, cfg, root)
	instances, err = second.catalog.Read()
	if err != nil || len(instances) != 1 || len(instances[0].Components) != 1 || instances[0].Components[0].LogCount != 3 {
		t.Fatalf("重新初始化改写了历史记录: %+v, %v", instances, err)
	}
	if _, err := second.Register(ComponentConfig{Name: "rpc", Description: "新区块获取"}); err != nil {
		t.Fatal(err)
	}
	instances, err = second.catalog.Read()
	if err != nil || len(instances[0].Components) != 1 || instances[0].Components[0].Name != "obsolete" {
		t.Fatalf("注册未使用组件改写了目录: got=%+v err=%v", instances, err)
	}
	c := testComponent(t, second, "obsolete")
	c.Info("重启后继续累计", nil)
	second.components["rpc"].Info("新区块", nil)
	_ = second.Close(context.Background())
	instances, err = second.catalog.Read()
	if err != nil || len(instances) != 1 || len(instances[0].Components) != 2 {
		t.Fatalf("重启后的实例组件不符: %+v, %v", instances, err)
	}
	instance := instances[0]
	if !instance.CreatedAt.Equal(createdAt) || instance.InstanceDescription != cfg.InstanceDescription {
		t.Fatalf("重启后的创建时间或描述不符: %+v", instance)
	}
	if instance.Components[0].LogCount != 4 || instance.Components[1].LogCount != 1 {
		t.Fatalf("重启累计或新增组件不符: %+v, %v", instances, err)
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
			component, err := m.Register(ComponentConfig{Name: fmt.Sprintf("component-%02d", i), Description: "并发注册"})
			if err != nil {
				t.Error(err)
				return
			}
			if i%2 == 0 {
				component.Info("实际使用", nil)
			}
		}(i)
	}
	workers.Wait()
	_ = m.Close(context.Background())
	instances, err := m.catalog.Read()
	if err != nil || len(instances) != 1 || len(instances[0].Components) != 6 {
		t.Fatalf("并发发布遗漏组件: %+v, %v", instances, err)
	}
	for _, component := range instances[0].Components {
		if component.Name == diagnostic.ComponentName || component.LogCount != 1 {
			t.Fatalf("状态维护或未使用组件被列入目录: %+v", component)
		}
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
	c.Info("需要持久化的入队日志", nil)
	waitUntil(t, func() bool {
		return strings.Contains(capture.String(), "发布日志名称目录失败")
	})
	if currentRecord(t, m, "rpc").State != int(StateHealthy) || currentRecord(t, m, diagnostic.ComponentName).State != int(StateHealthy) {
		t.Fatal("辅助目录失败改变了原有健康状态")
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	m.publishCatalog()
	instances, err := m.catalog.Read()
	if err != nil || len(instances) != 1 || instances[0].Components[0].LogCount != 1 {
		t.Fatalf("写入失败后重试丢失或重复累计: %+v, %v", instances, err)
	}
}

func TestCatalogConcurrentComponentCounts(t *testing.T) {
	cfg := testConfig(t)
	cfg.DefaultQueueCapacity = 4096
	m := catalogManager(t, cfg, t.TempDir())
	components := []*Component{testComponent(t, m, "a"), testComponent(t, m, "b")}
	var workers sync.WaitGroup
	for _, component := range components {
		for index := 0; index < 8; index++ {
			workers.Add(1)
			go func(component *Component) {
				defer workers.Done()
				for count := 0; count < 100; count++ {
					component.Info("并发入队", nil)
				}
			}(component)
		}
	}
	workers.Wait()
	_ = m.Close(context.Background())
	instances, err := m.catalog.Read()
	if err != nil || len(instances) != 1 || len(instances[0].Components) != 2 {
		t.Fatalf("组件独立计数目录不符: %+v, %v", instances, err)
	}
	for _, component := range instances[0].Components {
		if component.LogCount != 800 {
			t.Fatalf("组件并发数量不符: %+v", component)
		}
	}
}

func TestCatalogPollingPreservesActualActivity(t *testing.T) {
	m := catalogManager(t, testConfig(t), t.TempDir())
	c := testComponent(t, m, "rpc")
	c.Info("一次活动", nil)
	waitUntil(t, func() bool {
		instances, err := m.catalog.Read()
		return err == nil && len(instances) == 1
	})
	before, _ := m.catalog.Read()
	time.Sleep(time.Millisecond)
	m.publishCatalog()
	after, err := m.catalog.Read()
	if err != nil || !before[0].Components[0].LastActiveAt.Equal(after[0].Components[0].LastActiveAt) || after[0].Components[0].LogCount != 1 {
		t.Fatalf("轮询伪造了活动或重复累计: %+v, %v", after, err)
	}
}

func TestCatalogEncodingAndCloseRejectionsDoNotCount(t *testing.T) {
	m := catalogManager(t, testConfig(t), t.TempDir())
	c := testComponent(t, m, "rpc")
	c.Info("编码失败", Fields{"unsupported": func() {}})
	m.publishCatalog()
	instances, err := m.catalog.Read()
	if err != nil || len(instances) != 0 {
		t.Fatalf("未入队日志建立了目录: %+v, %v", instances, err)
	}
	c.Info("成功入队", nil)
	_ = m.Close(context.Background())
	c.Info("关闭拒收", nil)
	instances, err = m.catalog.Read()
	if err != nil || len(instances) != 1 || instances[0].Components[0].LogCount != 1 {
		t.Fatalf("拒收计入了成功入队数量: %+v, %v", instances, err)
	}
}

func TestCatalogStartupDiagnosticActivity(t *testing.T) {
	cfg := testConfig(t)
	cfg.CatalogEnabled = true
	cfg, err := cfg.normalized()
	if err != nil {
		t.Fatal(err)
	}
	m := newManager(cfg)
	m.catalog.Root = t.TempDir()
	m.registry.MustRegister(m)
	m.startupLogs = logging.New(8, cfg.SyslogSocketPath, cfg.Project, cfg.InstanceID, diagnostic.ComponentName, m.catalogWake)
	entry, err := logging.Encode(m.id, diagnostic.ComponentName, logging.Warning, "初始化配置诊断", nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.startupLogs.Enqueue(entry); err != nil {
		t.Fatal(err)
	}
	// 与 SafeNew 的启动顺序一致：初始化诊断先入队，再启动目录后台任务。
	m.startCatalog()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = m.Close(ctx)
	})
	waitUntil(t, func() bool {
		instances, err := m.catalog.Read()
		return err == nil && len(instances) == 1
	})
	_ = m.Close(context.Background())
	instances, err := m.catalog.Read()
	if err != nil || len(instances) != 1 || len(instances[0].Components) != 1 {
		t.Fatalf("实际初始化诊断没有建立目录: %+v, %v", instances, err)
	}
	component := instances[0].Components[0]
	if component.Name != diagnostic.ComponentName || component.LogCount != 1 {
		t.Fatalf("初始化诊断未按入队独立累计: %+v", component)
	}
}

func TestCatalogCloseDeadlineWhileProjectLocked(t *testing.T) {
	cfg := testConfig(t)
	m := catalogManager(t, cfg, t.TempDir())
	c := testComponent(t, m, "rpc")
	c.Info("建立目录", nil)
	waitUntil(t, func() bool {
		instances, err := m.catalog.Read()
		return err == nil && len(instances) == 1
	})
	directory := filepath.Dir(m.catalog.Path(cfg.Project, cfg.InstanceID))
	lock := flock.New(filepath.Join(directory, ".catalog.lock"))
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	c.Info("等待最终同步", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := m.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("持锁时 Close 未按上下文截止返回: %v", err)
	}
	select {
	case <-m.done:
		t.Fatal("项目锁尚未释放时提前结束了最终同步")
	default:
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finishCancel()
	_ = m.Close(finishCtx)
	select {
	case <-m.catalogDone:
	default:
		t.Fatal("解锁后目录后台任务没有退出")
	}
	select {
	case <-m.done:
	default:
		t.Fatal("解锁后管理器收尾没有完成")
	}
	instances, err := m.catalog.Read()
	if err != nil || len(instances) != 1 || instances[0].Components[0].LogCount != 2 {
		t.Fatalf("释放锁后没有完成最终同步: %+v, %v", instances, err)
	}
	metrics, err := m.registry.Gather()
	if err != nil || len(metrics) != 0 {
		t.Fatalf("关闭收尾后没有注销自身 Collector: %d, %v", len(metrics), err)
	}
}
