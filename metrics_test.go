package syslogger

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mctxyz/syslogger/internal/diagnostic"
	"github.com/mctxyz/syslogger/internal/record"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// metricFixture 通过实际管理器准备固定健康事实及已发生的日志失败。
func metricFixture(t *testing.T) *Manager {
	t.Helper()
	captureLogs(t)
	cfg := testConfig(t)
	cfg.Project = "项目"
	cfg.InstanceID = "a\"\\\nb"
	cfg.Registry = prometheus.NewPedanticRegistry()
	m := testManager(t, cfg)
	rpc := testComponent(t, m, "rpc")
	testComponent(t, m, "a")
	rpc.mu.Lock()
	rpc.current = record.New(int(StateUnhealthy), time.Unix(1792656000, 123))
	rpc.mu.Unlock()
	rpc.logs.Reject()
	rpc.logs.Reject()
	rpc.Info("独立测试 socket 发送失败", nil)
	waitUntil(t, func() bool {
		_, failed := rpc.logs.Counts()
		return failed == 1
	})
	return m
}

func TestNativeCollectorContract(t *testing.T) {
	m := metricFixture(t)
	families, err := m.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) != 4 {
		t.Fatalf("内置指标族数量不符: %d", len(families))
	}
	seen := make(map[string]bool)
	for _, family := range families {
		for _, metric := range family.Metric {
			labels := make(map[string]string)
			for _, label := range metric.Label {
				labels[label.GetName()] = label.GetValue()
			}
			if len(labels) != 3 || labels["project"] != m.cfg.Project || labels["instance_id"] != m.cfg.InstanceID {
				t.Fatalf("内置身份标签不符: %v", labels)
			}
			key := family.GetName() + ":" + labels["component"]
			if seen[key] {
				t.Fatalf("重复指标样本: %s", key)
			}
			seen[key] = true
			if strings.HasSuffix(family.GetName(), "_total") {
				if metric.Counter == nil || labels["component"] == diagnostic.ComponentName {
					t.Fatal("日志 Counter 的类型或管理器样本不符")
				}
			} else if metric.Gauge == nil {
				t.Fatal("健康事实未使用 Gauge")
			}
		}
	}
	if len(seen) != 10 {
		t.Fatalf("内置指标样本数量不符: %d", len(seen))
	}
	data, err := m.metricsBytes()
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{
		"project":     m.cfg.Project,
		"instance_id": m.cfg.InstanceID,
		"component":   "rpc",
	}
	for name, value := range map[string]float64{
		"syslogger_component_state":                   3,
		"syslogger_component_timestamp_seconds":       1792656000,
		"syslogger_component_log_rejected_total":      2,
		"syslogger_component_log_send_failures_total": 1,
	} {
		if !hasMetricSample(t, string(data), name, labels, value) {
			t.Fatalf("内置样本不符: %s", name)
		}
	}
	labels["component"] = "a"
	for _, definition := range metricDefinitions {
		if !hasMetricSample(t, string(data), definition.name, labels, 0) {
			t.Fatalf("初始组件样本不符: %s", definition.name)
		}
	}
}

func TestSharedRegistryExportsBusinessMetrics(t *testing.T) {
	registry := prometheus.NewPedanticRegistry()
	height := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "alertdog_node_height",
		Help: "节点高度",
	}, []string{"chain", "source"})
	requests := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "business_requests_total",
		Help: "业务请求次数",
	})
	duration := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "business_duration_seconds",
		Help: "业务处理耗时",
	})
	registry.MustRegister(height, requests, duration)
	height.WithLabelValues("eth", "mct").Set(123)
	requests.Add(2)
	duration.Observe(0.25)
	cfg := testConfig(t)
	cfg.Registry = registry
	cfg.HTTP.Enabled = true
	cfg.File.Enabled = true
	m := testManager(t, cfg)
	if m.Registry() != registry {
		t.Fatal("未使用调用方提供的注册表")
	}
	testComponent(t, m, "rpc").Unhealthy()
	handler, err := m.Handler()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.StartFile(); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	file := readSnapshot(t, cfg.File.OutputPath)
	if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), file) {
		t.Fatal("HTTP 与文件未导出相同的共享指标集合")
	}
	labels := map[string]string{"chain": "eth", "source": "mct"}
	if !hasMetricSample(t, string(file), "alertdog_node_height", labels, 123) {
		t.Fatal("业务 GaugeVec 的数值或原有标签被改变")
	}
	if !hasMetricSample(t, string(file), "business_requests_total", nil, 2) {
		t.Fatal("业务 Counter 未导出")
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	foundHistogram := false
	for _, family := range families {
		if family.GetName() == "business_duration_seconds" {
			metric := family.Metric[0].GetHistogram()
			foundHistogram = metric.GetSampleCount() == 1 && metric.GetSampleSum() == 0.25
		}
	}
	if !foundHistogram {
		t.Fatal("业务 Histogram 未通过标准采集导出")
	}
	external := promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
	response = httptest.NewRecorder()
	external.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), file) {
		t.Fatal("外部 promhttp 未同时导出内置与业务指标")
	}
	if _, err := m.Register("late", 1); err == nil {
		t.Fatal("导出开始后仍允许增加业务组件")
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(file, readSnapshot(t, cfg.File.OutputPath)) {
		t.Fatal("最终文件未保留关闭截止前的完整指标")
	}
	// 库关闭后业务注册与更新继续有效，已有业务 HTTP 服务仍可使用同一注册表。
	height.WithLabelValues("eth", "mct").Set(456)
	response = httptest.NewRecorder()
	external.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if response.Code != http.StatusOK || !hasMetricSample(t, response.Body.String(), "alertdog_node_height", labels, 456) {
		t.Fatal("关闭管理器影响了业务 Collector 或外部 HTTP 服务")
	}
	if strings.Contains(response.Body.String(), "syslogger_component_") {
		t.Fatal("关闭管理器后自身 Collector 仍留在共享注册表")
	}
}

func TestRegistryIsolationAndSharedInstances(t *testing.T) {
	a := testManager(t, testConfig(t))
	b := testManager(t, testConfig(t))
	if a.Registry() == b.Registry() {
		t.Fatal("默认管理器共享了注册表")
	}
	registry := prometheus.NewRegistry()
	cfg := testConfig(t)
	cfg.Registry = registry
	cfg.InstanceID = "instance-a"
	first := testManager(t, cfg)
	cfg.InstanceID = "instance-b"
	second := testManager(t, cfg)
	testComponent(t, first, "rpc").Healthy()
	testComponent(t, second, "rpc").Unhealthy()
	data, err := first.metricsBytes()
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{
		"project":     cfg.Project,
		"instance_id": "instance-a",
		"component":   "rpc",
	}
	if !hasMetricSample(t, string(data), "syslogger_component_state", labels, 1) {
		t.Fatal("共享注册表丢失首个实例的状态")
	}
	labels["instance_id"] = "instance-b"
	if !hasMetricSample(t, string(data), "syslogger_component_state", labels, 3) {
		t.Fatal("共享注册表未隔离不同实例的标签")
	}
	if err := first.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, err = second.metricsBytes()
	if err != nil {
		t.Fatal(err)
	}
	if !hasMetricSample(t, string(data), "syslogger_component_state", labels, 3) || strings.Contains(string(data), `instance_id="instance-a"`) {
		t.Fatal("关闭单个实例误删其他实例或残留自身指标")
	}
}

func TestRegistryGatherFreezesComponents(t *testing.T) {
	m := testManager(t, testConfig(t))
	registry := m.Registry()
	testComponent(t, m, "rpc").Healthy()
	if _, err := registry.Gather(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Register("late", 1); err == nil {
		t.Fatal("首次 Registry 采集未固定组件集合")
	}
	metric := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "business_late_value",
		Help: "业务后续注册的数值",
	})
	if err := registry.Register(metric); err != nil {
		t.Fatalf("固定组件集合影响了业务 Collector 注册: %v", err)
	}
	metric.Set(7)
	data, err := m.metricsBytes()
	if err != nil {
		t.Fatal(err)
	}
	if !hasMetricSample(t, string(data), "business_late_value", nil, 7) {
		t.Fatal("后续业务 Collector 未参与采集")
	}
}

func TestRegistryCollisionAndSafeNew(t *testing.T) {
	capture := captureLogs(t)
	cfg := testConfig(t)
	cfg.Registry = prometheus.NewRegistry()
	first := testManager(t, cfg)
	if manager, err := New(cfg); manager != nil || err == nil {
		t.Fatal("严格构造未拒绝相同身份的 Collector 冲突")
	} else {
		var duplicate prometheus.AlreadyRegisteredError
		if !errors.As(err, &duplicate) {
			t.Fatalf("注册冲突未保留官方错误身份: %v", err)
		}
	}
	safe := testSafeManager(t, cfg)
	if safe.Registry() == cfg.Registry || safe.cfg.Registry != nil || first.Registry() != cfg.Registry {
		t.Fatal("容错构造未独立恢复注册表或修改了原管理器")
	}
	testComponent(t, safe, "rpc").Healthy()
	if currentRecord(t, safe, diagnostic.ComponentName).State != int(StateUnhealthy) {
		t.Fatal("注册表纠正未保留管理器初始化异常")
	}
	if !strings.Contains(capture.String(), `field="Registry"`) {
		t.Fatal("注册表纠正未输出标准日志诊断")
	}
	if _, err := safe.Registry().Gather(); err != nil {
		t.Fatalf("容错构造的独立注册表不可用: %v", err)
	}
}

// callbackCollector 用标准 Collector 契约模拟业务采集错误及对管理器的并发访问。
type callbackCollector struct {
	desc    *prometheus.Desc
	collect func(chan<- prometheus.Metric)
}

// Describe 提供测试业务指标的稳定描述符。
func (c *callbackCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.desc
}

// Collect 执行业务侧采集逻辑，生产代码继续直接使用官方注册表。
func (c *callbackCollector) Collect(ch chan<- prometheus.Metric) {
	c.collect(ch)
}

func TestGatherFailurePreservesFileAndReportsHTTP(t *testing.T) {
	capture := captureLogs(t)
	cfg := testConfig(t)
	cfg.HTTP.Enabled = true
	cfg.File.Enabled = true
	cfg.File.FlushInterval = 5 * time.Millisecond
	m := testManager(t, cfg)
	component := testComponent(t, m, "rpc")
	component.Healthy()
	var failing atomic.Bool
	collectErr := errors.New("业务采集失败")
	desc := prometheus.NewDesc("business_probe", "业务采集状态", nil, nil)
	m.Registry().MustRegister(&callbackCollector{
		desc: desc,
		collect: func(ch chan<- prometheus.Metric) {
			if failing.Load() {
				ch <- prometheus.NewInvalidMetric(desc, collectErr)
				return
			}
			_ = m.snapshot()
			ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, 1)
		},
	})
	handler, err := m.Handler()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.StartFile(); err != nil {
		t.Fatal(err)
	}
	before := readSnapshot(t, cfg.File.OutputPath)
	failing.Store(true)
	select {
	case <-capture.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("周期采集失败未通过标准日志诊断")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatal("采集错误时 HTTP 返回了成功或不完整结果")
	}
	if err := m.flush(); !errors.Is(err, collectErr) {
		t.Fatalf("文件采集错误未保留失败原因: %v", err)
	}
	if !bytes.Equal(before, readSnapshot(t, cfg.File.OutputPath)) {
		t.Fatal("采集失败破坏了旧的完整文件")
	}
	if err := m.Close(context.Background()); !errors.Is(err, collectErr) {
		t.Fatalf("最终文件采集未返回原始错误: %v", err)
	}
	if !bytes.Equal(before, readSnapshot(t, cfg.File.OutputPath)) {
		t.Fatal("最终采集失败破坏了旧的完整文件")
	}
	failing.Store(false)
	families, err := m.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) != 1 || families[0].GetName() != "business_probe" {
		t.Fatal("最终采集失败影响了自身注销或业务 Collector 生命周期")
	}
}

func TestStartFileReportsGatherFailure(t *testing.T) {
	captureLogs(t)
	cfg := testConfig(t)
	cfg.File.Enabled = true
	m := testManager(t, cfg)
	testComponent(t, m, "rpc")
	desc := prometheus.NewDesc("business_probe", "业务采集状态", nil, nil)
	collectErr := errors.New("业务首次采集失败")
	collector := &callbackCollector{
		desc: desc,
		collect: func(ch chan<- prometheus.Metric) {
			ch <- prometheus.NewInvalidMetric(desc, collectErr)
		},
	}
	m.Registry().MustRegister(collector)
	if err := m.StartFile(); !errors.Is(err, collectErr) {
		t.Fatalf("首次文件采集未返回原始错误: %v", err)
	}
	if _, err := os.Stat(cfg.File.OutputPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("首次采集失败仍写入了不完整文件: %v", err)
	}
	m.Registry().Unregister(collector)
	if err := m.StartFile(); err != nil {
		t.Fatalf("业务恢复后无法重试启动文件出口: %v", err)
	}
}

func TestLongIdentityValues(t *testing.T) {
	for _, safe := range []bool{false, true} {
		cfg := testConfig(t)
		cfg.Project = strings.Repeat("项", 2048)
		var manager *Manager
		if safe {
			manager = testSafeManager(t, cfg)
		} else {
			manager = testManager(t, cfg)
		}
		name := strings.Repeat("组", 2048)
		testComponent(t, manager, name).Healthy()
		data, err := manager.metricsBytes()
		if err != nil {
			t.Fatal(err)
		}
		labels := map[string]string{
			"project":     cfg.Project,
			"instance_id": cfg.InstanceID,
			"component":   name,
		}
		if !hasMetricSample(t, string(data), "syslogger_component_state", labels, 1) {
			t.Fatal("长身份值被拒绝、替换或截断")
		}
	}
}

func TestPromtoolContract(t *testing.T) {
	path, err := exec.LookPath("promtool")
	if err != nil {
		t.Skip("当前工具链未安装 promtool")
	}
	data, err := metricFixture(t).metricsBytes()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(path, "check", "metrics")
	command.Stdin = bytes.NewReader(data)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("promtool 检查失败: %v\n%s", err, output)
	}
}
