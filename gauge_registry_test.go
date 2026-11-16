package syslogger

import (
	"context"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestGaugeRegistryTypeConflictsUseOfficialGatherErrors(t *testing.T) {
	for _, order := range []string{"counter_first", "gauge_first"} {
		t.Run(order, func(t *testing.T) {
			cfg := testConfig(t)
			cfg.Registry = prometheus.NewPedanticRegistry()
			m := testManager(t, cfg)
			component := testComponent(t, m, "worker")
			counter := prometheus.NewCounter(prometheus.CounterOpts{
				Name: "syslogger_component_quantity",
				Help: "当前数量",
				ConstLabels: prometheus.Labels{
					"project": "external", "instance_id": "external", "component": "worker", "name": "external",
				},
			})
			if order == "counter_first" {
				m.Registry().MustRegister(counter)
			}
			gauge := testGauge(t, component, GaugeConfig{Metric: "quantity", Name: "pending", Help: "当前数量"})
			if order == "gauge_first" {
				m.Registry().MustRegister(counter)
			}
			if err := gauge.Set(1); err != nil {
				t.Fatal(err)
			}
			counter.Inc()
			// 如果注册过程进行了 Gather，组件集合会提前冻结。
			if _, err := m.Register(ComponentConfig{Name: "after-gauge"}); err != nil {
				t.Fatalf("注册 Gauge 提前触发了采集: %v", err)
			}
			if _, err := m.Registry().Gather(); err == nil {
				t.Fatal("外部同名跨类型冲突没有按官方采集语义报告")
			}
			if err := m.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			families, err := m.Registry().Gather()
			if err != nil || len(families) != 1 || families[0].Metric[0].Counter == nil {
				t.Fatalf("关闭后未仅保留外部 Counter: %v", err)
			}
		})
	}
}

func TestGaugeUsesSafeNewEffectiveRegistry(t *testing.T) {
	captureLogs(t)
	cfg := testConfig(t)
	cfg.Registry = prometheus.NewPedanticRegistry()
	strict := testManager(t, cfg)
	first := testGauge(t, testComponent(t, strict, "worker"), GaugeConfig{Metric: "quantity", Name: "pending"})
	if err := first.Set(9); err != nil {
		t.Fatal(err)
	}
	safe := testSafeManager(t, cfg)
	if safe.Registry() == cfg.Registry {
		t.Fatal("SafeNew 没有回退到独立 Registry")
	}
	second := testGauge(t, testComponent(t, safe, "worker"), GaugeConfig{Metric: "quantity", Name: "pending"})
	if err := second.Set(4); err != nil {
		t.Fatal(err)
	}
	identity := map[string]string{"project": cfg.Project, "instance_id": cfg.InstanceID, "component": "worker", "name": "pending"}
	for manager, want := range map[*Manager]float64{strict: 9, safe: 4} {
		data, err := manager.metricsBytes()
		if err != nil || !hasMetricSample(t, string(data), "syslogger_component_quantity", identity, want) {
			t.Fatalf("数量指标没有使用管理器实际 Registry: want=%v err=%v", want, err)
		}
	}
	if currentRecord(t, safe, "syslogger_manager").State != int(StateUnhealthy) {
		t.Fatal("Gauge 注册覆盖了 SafeNew 初始化异常")
	}
	if err := safe.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, err := strict.metricsBytes()
	if err != nil || !strings.Contains(string(data), "syslogger_component_quantity") {
		t.Fatalf("关闭回退表影响了原 Registry: %v", err)
	}
}
