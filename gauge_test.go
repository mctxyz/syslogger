package syslogger

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// testGauge 通过公开注册入口创建测试数量指标，不注入正式代码的内部状态。
func testGauge(t *testing.T, component *Component, cfg GaugeConfig) *Gauge {
	t.Helper()
	gauge, err := component.RegisterGauge(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return gauge
}

func TestGaugeIdentityAndIndependentValues(t *testing.T) {
	cfg := testConfig(t)
	cfg.Project = "项目\"\n"
	cfg.InstanceID = "实例-a"
	cfg.Registry = prometheus.NewPedanticRegistry()
	m := testManager(t, cfg)
	a := testComponent(t, m, "worker\nA")
	labels := []string{"priority", "chain"}
	pending := testGauge(t, a, GaugeConfig{Metric: "queue_length", Name: "pending", Help: "队列当前任务数", LabelNames: labels})
	labels[0] = "changed"
	// Gauge 注册不触发采集或冻结，后续组件及同族不同对象仍可注册。
	b := testComponent(t, m, "worker-B")
	retry := testGauge(t, a, GaugeConfig{Metric: "queue_length", Name: "retry", Help: "队列当前任务数", LabelNames: []string{"chain", "priority"}})
	other := testGauge(t, b, GaugeConfig{Metric: "queue_length", Name: "pending", Help: "队列当前任务数", LabelNames: []string{"priority", "chain"}})
	overall := testGauge(t, a, GaugeConfig{Metric: "queue_length", Name: "overall", Help: "队列当前任务数", LabelNames: []string{"priority", "chain"}})
	connections := testGauge(t, a, GaugeConfig{Metric: "active_connections", Name: "pending", Help: "当前连接数"})
	families, err := m.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() == "syslogger_component_queue_length" || family.GetName() == "syslogger_component_active_connections" {
			t.Fatal("未报告数量时提前生成了零值样本")
		}
	}
	values := Labels{"priority": "high", "chain": "eth"}
	if err := pending.Set(42, values); err != nil {
		t.Fatal(err)
	}
	values["priority"] = "changed"
	for _, report := range []struct {
		gauge    *Gauge
		value    float64
		priority string
	}{
		{pending, 5, "low"},
		{retry, 7, "high"},
		{other, 8, "high"},
		{overall, 9, "high"},
	} {
		if err := report.gauge.Set(report.value, Labels{"priority": report.priority, "chain": "eth"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := connections.Set(12); err != nil {
		t.Fatal(err)
	}
	if err := connections.Set(0, Labels{}); err != nil {
		t.Fatal(err)
	}
	data, err := m.metricsBytes()
	if err != nil {
		t.Fatal(err)
	}
	for _, sample := range []struct {
		component string
		name      string
		priority  string
		value     float64
	}{
		{a.name, "pending", "high", 42},
		{a.name, "pending", "low", 5},
		{a.name, "retry", "high", 7},
		{b.name, "pending", "high", 8},
		{a.name, "overall", "high", 9},
	} {
		identity := map[string]string{
			"project": cfg.Project, "instance_id": cfg.InstanceID, "component": sample.component,
			"name": sample.name, "priority": sample.priority, "chain": "eth",
		}
		if !hasMetricSample(t, string(data), "syslogger_component_queue_length", identity, sample.value) {
			t.Fatalf("数量值、身份或业务标签不符: %+v", sample)
		}
	}
	identity := map[string]string{"project": cfg.Project, "instance_id": cfg.InstanceID, "component": a.name, "name": "pending"}
	if !hasMetricSample(t, string(data), "syslogger_component_active_connections", identity, 0) {
		t.Fatal("不同指标的同名对象相互覆盖，或没有保留显式零值")
	}
	families, err = m.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() == "syslogger_component_queue_length" {
			if family.GetHelp() != "队列当前任务数" || len(family.Metric) != 5 {
				t.Fatalf("指标定义或样本数量错误: %+v", family)
			}
			for _, metric := range family.Metric {
				if metric.Gauge == nil {
					t.Fatal("业务数量未按 Gauge 导出")
				}
			}
		}
	}
}

func TestGaugeRejectsInvalidDefinitions(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*GaugeConfig)
	}{
		{"empty_metric", func(cfg *GaugeConfig) { cfg.Metric = "" }},
		{"invalid_metric", func(cfg *GaugeConfig) { cfg.Metric = "bad metric" }},
		{"full_prefix", func(cfg *GaugeConfig) { cfg.Metric = "syslogger_component_queue_length" }},
		{"state", func(cfg *GaugeConfig) { cfg.Metric = "state" }},
		{"timestamp", func(cfg *GaugeConfig) { cfg.Metric = "timestamp_seconds" }},
		{"rejected", func(cfg *GaugeConfig) { cfg.Metric = "log_rejected_total" }},
		{"failures", func(cfg *GaugeConfig) { cfg.Metric = "log_send_failures_total" }},
		{"empty_name", func(cfg *GaugeConfig) { cfg.Name = "" }},
		{"blank_name", func(cfg *GaugeConfig) { cfg.Name = " \t" }},
		{"invalid_name", func(cfg *GaugeConfig) { cfg.Name = "bad\xff" }},
		{"project", func(cfg *GaugeConfig) { cfg.LabelNames = []string{"project"} }},
		{"instance_id", func(cfg *GaugeConfig) { cfg.LabelNames = []string{"instance_id"} }},
		{"component", func(cfg *GaugeConfig) { cfg.LabelNames = []string{"component"} }},
		{"name", func(cfg *GaugeConfig) { cfg.LabelNames = []string{"name"} }},
		{"internal_label", func(cfg *GaugeConfig) { cfg.LabelNames = []string{"__internal"} }},
		{"duplicate_label", func(cfg *GaugeConfig) { cfg.LabelNames = []string{"priority", "priority"} }},
		{"invalid_label", func(cfg *GaugeConfig) { cfg.LabelNames = []string{"bad-label"} }},
		{"empty_label", func(cfg *GaugeConfig) { cfg.LabelNames = []string{""} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := testManager(t, testConfig(t))
			component := testComponent(t, m, "worker")
			definition := GaugeConfig{Metric: "queue_length", Name: "pending", Help: "队列当前任务数"}
			test.change(&definition)
			if gauge, err := component.RegisterGauge(definition); err == nil || gauge != nil || len(m.gauges) != 0 {
				t.Fatalf("非法定义未拒收或错误登记了资源: gauge=%v err=%v", gauge, err)
			}
			testGauge(t, component, GaugeConfig{Metric: "valid_quantity", Name: "pending"})
		})
	}
}

func TestGaugeConflictsAndInvalidLabelsPreserveExistingValues(t *testing.T) {
	m := testManager(t, testConfig(t))
	component := testComponent(t, m, "worker")
	component.Healthy("service")
	before := currentRecord(t, m, "worker")
	diagnostic := currentRecord(t, m, "syslogger_manager")
	definition := GaugeConfig{Metric: "queue_length", Name: "pending", Help: "队列当前任务数", LabelNames: []string{"priority"}}
	gauge := testGauge(t, component, definition)
	if err := gauge.Set(12, Labels{"priority": "high"}); err != nil {
		t.Fatal(err)
	}
	_, err := component.RegisterGauge(definition)
	var duplicate prometheus.AlreadyRegisteredError
	if !errors.As(err, &duplicate) {
		t.Fatalf("重复注册没有保留官方错误: %v", err)
	}
	for _, conflict := range []GaugeConfig{
		{Metric: definition.Metric, Name: "other", Help: "不同说明", LabelNames: []string{"priority"}},
		{Metric: definition.Metric, Name: "other", Help: definition.Help, LabelNames: []string{"region"}},
	} {
		if _, err := component.RegisterGauge(conflict); err == nil {
			t.Fatal("同名指标的不一致定义未拒收")
		}
	}
	for _, labels := range [][]Labels{
		nil,
		{{}},
		{{"region": "high"}},
		{{"priority": "high", "name": "override"}},
		{{"priority": "high"}, {"priority": "low"}},
	} {
		if err := gauge.Set(99, labels...); err == nil {
			t.Fatalf("无效标签未拒收: %v", labels)
		}
	}
	if len(m.gauges) != 1 || currentRecord(t, m, "worker") != before || currentRecord(t, m, "syslogger_manager") != diagnostic {
		t.Fatal("数量指标错误修改了资源归属或健康记录")
	}
	if rejected, failed := component.logs.Counts(); rejected != 0 || failed != 0 {
		t.Fatal("数量指标错误改变了日志 Counter")
	}
	data, err := m.metricsBytes()
	if err != nil {
		t.Fatal(err)
	}
	identity := map[string]string{"project": m.id.Project, "instance_id": m.id.InstanceID, "component": "worker", "name": "pending", "priority": "high"}
	if !hasMetricSample(t, string(data), "syslogger_component_queue_length", identity, 12) {
		t.Fatal("注册冲突或无效标签覆盖了原值")
	}
}

func TestGaugeInvalidHandlesAndNumericValues(t *testing.T) {
	var nilComponent *Component
	for _, component := range []*Component{nilComponent, {}} {
		if gauge, err := component.RegisterGauge(GaugeConfig{Metric: "quantity", Name: "value"}); err == nil || gauge != nil {
			t.Fatal("无效组件注册了数量指标")
		}
	}
	var nilGauge *Gauge
	for _, gauge := range []*Gauge{nilGauge, {}} {
		if err := gauge.Set(1); err == nil {
			t.Fatal("无效 Gauge 静默接收了数量")
		}
	}
	m := testManager(t, testConfig(t))
	gauge := testGauge(t, testComponent(t, m, "worker"), GaugeConfig{Metric: "quantity", Name: "value"})
	for _, value := range []float64{0, -1.5, math.Inf(1), math.NaN()} {
		if err := gauge.Set(value); err != nil {
			t.Fatal(err)
		}
		families, err := m.Registry().Gather()
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, family := range families {
			if family.GetName() != "syslogger_component_quantity" {
				continue
			}
			found = true
			got := family.Metric[0].GetGauge().GetValue()
			if got != value && !(math.IsNaN(got) && math.IsNaN(value)) {
				t.Fatalf("偏离官方 Gauge 数值语义: got=%v want=%v", got, value)
			}
		}
		if !found {
			t.Fatal("数量样本没有导出")
		}
	}
	if data, err := m.metricsBytes(); err != nil || !strings.Contains(string(data), "NaN") {
		t.Fatalf("官方非有限值编码被改变: %v", err)
	}
}
