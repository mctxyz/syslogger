package syslogger

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func TestGaugeRegistrationFreezesButValuesRemainWritable(t *testing.T) {
	for _, mode := range []string{"handler", "file", "gather", "failed_http_start"} {
		t.Run(mode, func(t *testing.T) {
			cfg := testConfig(t)
			cfg.HTTP.Enabled = true
			cfg.File.Enabled = true
			m := testManager(t, cfg)
			component := testComponent(t, m, "worker")
			gauge := testGauge(t, component, GaugeConfig{Metric: "queue_length", Name: "pending", LabelNames: []string{"priority"}})
			switch mode {
			case "handler":
				if _, err := m.Handler(); err != nil {
					t.Fatal(err)
				}
			case "file":
				if err := m.StartFile(); err != nil {
					t.Fatal(err)
				}
			case "gather":
				if _, err := m.Registry().Gather(); err != nil {
					t.Fatal(err)
				}
			case "failed_http_start":
				// 缺少监听地址时原启动流程仍已冻结定义，测试不启动真实监听。
				if err := m.StartHTTP(); err == nil {
					t.Fatal("没有监听地址却启动了 HTTP")
				}
			}
			if next, err := component.RegisterGauge(GaugeConfig{Metric: "another", Name: "late"}); err == nil || next != nil {
				t.Fatal("冻结后仍注册了新指标定义")
			}
			if err := gauge.Set(12, Labels{"priority": "high"}); err != nil {
				t.Fatalf("冻结定义阻止了首次数量报告: %v", err)
			}
			if err := gauge.Set(3, Labels{"priority": "low"}); err != nil {
				t.Fatalf("冻结定义阻止了新的业务标签值: %v", err)
			}
		})
	}
}

func TestGaugeIndependentFromHealthSwitches(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		cfg := testConfig(t)
		cfg.StateEnabled = enabled
		m := testManager(t, cfg)
		component, err := m.Register(ComponentConfig{Name: "worker"})
		if err != nil {
			t.Fatal(err)
		}
		gauge := testGauge(t, component, GaugeConfig{Metric: "quantity", Name: "pending"})
		component.Unhealthy("service")
		if err := gauge.Set(5); err != nil {
			t.Fatal(err)
		}
		data, err := m.metricsBytes()
		if err != nil {
			t.Fatal(err)
		}
		identity := map[string]string{"project": cfg.Project, "instance_id": cfg.InstanceID, "component": "worker", "name": "pending"}
		assertComponentMetricSet(t, string(data), identity, map[string]float64{"syslogger_component_quantity": 5})
		identity["name"] = "service"
		assertComponentMetricSet(t, string(data), identity, map[string]float64{})
		identity["name"] = "overall"
		assertComponentMetricSet(t, string(data), identity, map[string]float64{})
	}
}

func TestGaugeOutletsAndSharedRegistryOwnership(t *testing.T) {
	cfg := testConfig(t)
	cfg.HTTP.Enabled = true
	cfg.File.Enabled = true
	cfg.Registry = prometheus.NewPedanticRegistry()
	m := testManager(t, cfg)
	component := testComponent(t, m, "worker")
	gauge := testGauge(t, component, GaugeConfig{Metric: "queue_length", Name: "pending", Help: "队列当前任务数"})
	otherCfg := cfg
	otherCfg.InstanceID = "instance-b"
	otherCfg.File.Enabled = false
	other := testManager(t, otherCfg)
	otherGauge := testGauge(t, testComponent(t, other, "worker"), GaugeConfig{Metric: "queue_length", Name: "pending", Help: "队列当前任务数"})
	business := prometheus.NewGauge(prometheus.GaugeOpts{Name: "business_quantity", Help: "外部业务数量"})
	m.Registry().MustRegister(business)
	if err := gauge.Set(11); err != nil {
		t.Fatal(err)
	}
	if err := otherGauge.Set(22); err != nil {
		t.Fatal(err)
	}
	business.Set(3)
	handler, err := m.Handler()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.StartFile(); err != nil {
		t.Fatal(err)
	}
	file := readSnapshot(t, cfg.File.OutputPath)
	for _, outlet := range []http.Handler{handler, promhttp.HandlerFor(m.Registry(), promhttp.HandlerOpts{})} {
		response := httptest.NewRecorder()
		outlet.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), file) {
			t.Fatal("数量指标在文件、SDK HTTP 或直接 promhttp 中不一致")
		}
	}
	if err := gauge.Set(13); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	final := readSnapshot(t, cfg.File.OutputPath)
	identity := map[string]string{"project": cfg.Project, "instance_id": cfg.InstanceID, "component": "worker", "name": "pending"}
	if !hasMetricSample(t, string(final), "syslogger_component_queue_length", identity, 13) {
		t.Fatal("最终文件没有包含周期快照之后接收的数量值")
	}
	if err := gauge.Set(99); !errors.Is(err, ErrClosed) {
		t.Fatalf("关闭后仍修改数量: %v", err)
	}
	if !bytes.Equal(final, readSnapshot(t, cfg.File.OutputPath)) {
		t.Fatal("关闭后的更新改变了最终文件")
	}
	if next, err := component.RegisterGauge(GaugeConfig{Metric: "late", Name: "late"}); !errors.Is(err, ErrClosed) || next != nil {
		t.Fatalf("关闭后仍注册指标: %v", err)
	}
	if err := otherGauge.Set(23); err != nil {
		t.Fatal(err)
	}
	business.Set(4)
	data, err := other.metricsBytes()
	if err != nil {
		t.Fatal(err)
	}
	assertComponentMetricSet(t, string(data), identity, map[string]float64{})
	identity["instance_id"] = otherCfg.InstanceID
	assertComponentMetricSet(t, string(data), identity, map[string]float64{"syslogger_component_queue_length": 23})
	if !hasMetricSample(t, string(data), "business_quantity", nil, 4) {
		t.Fatal("注销自身 Gauge 影响了外部业务指标")
	}
}

func TestGaugeCleanupAfterFinalGatherFailure(t *testing.T) {
	captureLogs(t)
	cfg := testConfig(t)
	cfg.File.Enabled = true
	cfg.Registry = prometheus.NewPedanticRegistry()
	m := testManager(t, cfg)
	gauge := testGauge(t, testComponent(t, m, "worker"), GaugeConfig{Metric: "quantity", Name: "pending"})
	otherCfg := cfg
	otherCfg.InstanceID = "instance-b"
	otherCfg.File.Enabled = false
	other := testManager(t, otherCfg)
	otherGauge := testGauge(t, testComponent(t, other, "worker"), GaugeConfig{Metric: "quantity", Name: "pending"})
	if err := gauge.Set(11); err != nil {
		t.Fatal(err)
	}
	if err := otherGauge.Set(22); err != nil {
		t.Fatal(err)
	}
	collectErr := errors.New("业务采集失败")
	var failing atomic.Bool
	desc := prometheus.NewDesc("business_probe", "业务探测", nil, nil)
	m.Registry().MustRegister(&callbackCollector{
		desc: desc,
		collect: func(ch chan<- prometheus.Metric) {
			if failing.Load() {
				ch <- prometheus.NewInvalidMetric(desc, collectErr)
				return
			}
			ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, 1)
		},
	})
	if err := m.StartFile(); err != nil {
		t.Fatal(err)
	}
	file := readSnapshot(t, cfg.File.OutputPath)
	failing.Store(true)
	if err := m.Close(context.Background()); !errors.Is(err, collectErr) {
		t.Fatalf("最终采集失败的原因丢失: %v", err)
	}
	if !bytes.Equal(file, readSnapshot(t, cfg.File.OutputPath)) {
		t.Fatal("失败的最终采集覆盖了既有文件")
	}
	failing.Store(false)
	data, err := other.metricsBytes()
	if err != nil {
		t.Fatal(err)
	}
	identity := map[string]string{"project": cfg.Project, "instance_id": cfg.InstanceID, "component": "worker", "name": "pending"}
	assertComponentMetricSet(t, string(data), identity, map[string]float64{})
	identity["instance_id"] = otherCfg.InstanceID
	assertComponentMetricSet(t, string(data), identity, map[string]float64{"syslogger_component_quantity": 22})
	if !hasMetricSample(t, string(data), "business_probe", nil, 1) {
		t.Fatal("关闭错误移除了外部 Collector")
	}
}

func TestGaugeConcurrentSetGatherRegisterAndClose(t *testing.T) {
	m := testManager(t, testConfig(t))
	component := testComponent(t, m, "worker")
	gauge := testGauge(t, component, GaugeConfig{Metric: "quantity", Name: "pending", LabelNames: []string{"priority"}})
	start := make(chan struct{})
	firstWrites := make(chan struct{}, 4)
	var workers sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			<-start
			for attempt := 0; attempt < 100; attempt++ {
				err := gauge.Set(float64(attempt), Labels{"priority": fmt.Sprint(worker % 2)})
				if err != nil && !errors.Is(err, ErrClosed) {
					t.Error(err)
				}
				if attempt == 0 {
					firstWrites <- struct{}{}
				}
			}
		}(worker)
	}
	workers.Add(3)
	go func() {
		defer workers.Done()
		<-start
		for attempt := 0; attempt < 20; attempt++ {
			if _, err := m.Registry().Gather(); err != nil {
				t.Error(err)
			}
		}
	}()
	go func() {
		defer workers.Done()
		<-start
		for attempt := 0; attempt < 20; attempt++ {
			gauge, err := component.RegisterGauge(GaugeConfig{Metric: "quantity", Name: fmt.Sprint(attempt), LabelNames: []string{"priority"}})
			if err == nil {
				if err := gauge.Set(1, Labels{"priority": "0"}); err != nil && !errors.Is(err, ErrClosed) {
					t.Error(err)
				}
			}
		}
	}()
	go func() {
		defer workers.Done()
		<-start
		// 每个更新任务至少真实写入一次，再让关闭与后续更新竞争。
		for worker := 0; worker < 4; worker++ {
			<-firstWrites
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.Close(ctx); err != nil {
			t.Error(err)
		}
	}()
	close(start)
	done := make(chan struct{})
	go func() {
		workers.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("注册、采集或关闭的锁序导致阻塞")
	}
	if families, err := m.Registry().Gather(); err != nil || len(families) != 0 {
		t.Fatalf("并发关闭遗漏了已注册资源: count=%d err=%v", len(families), err)
	}
}
