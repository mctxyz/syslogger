package syslogger

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mctxyz/syslogger/internal/diagnostic"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/expfmt"
)

// assertComponentMetricSet 按完整身份比较实际样本集合，确保关闭后样本缺失而非仅输出零值。
func assertComponentMetricSet(t *testing.T, text string, labels map[string]string, want map[string]float64) {
	t.Helper()
	var parser expfmt.TextParser
	families, err := parser.TextToMetricFamilies(strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]float64)
	for name, family := range families {
		for _, metric := range family.Metric {
			actualLabels := make(map[string]string)
			for _, label := range metric.Label {
				actualLabels[label.GetName()] = label.GetValue()
			}
			if !reflect.DeepEqual(actualLabels, labels) {
				continue
			}
			if metric.Gauge != nil {
				got[name] = metric.GetGauge().GetValue()
			} else if metric.Counter != nil {
				got[name] = metric.GetCounter().GetValue()
			} else {
				t.Fatalf("组件内置样本类型不符: %s", name)
			}
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("组件样本集合不符: labels=%v got=%v want=%v", labels, got, want)
	}
}

func TestComponentHealthMetricsOptIn(t *testing.T) {
	for _, constructor := range []string{"New", "SafeNew"} {
		for _, stateEnabled := range []bool{false, true} {
			name := constructor + "/state-off"
			if stateEnabled {
				name = constructor + "/state-on"
			}
			t.Run(name, func(t *testing.T) {
				cfg := testConfig(t)
				cfg.StateEnabled = stateEnabled
				cfg.Registry = prometheus.NewPedanticRegistry()
				var m *Manager
				if constructor == "SafeNew" {
					m = SafeNew(cfg)
					t.Cleanup(func() { _ = m.Close(context.Background()) })
				} else {
					m = testManager(t, cfg)
				}
				// 默认组件直接注册，不使用显式启用健康导出的公共测试助手。
				plain, err := m.Register(ComponentConfig{Name: "plain"})
				if err != nil {
					t.Fatal(err)
				}
				monitored, err := m.Register(ComponentConfig{Name: "monitored", EnableHealthMetrics: true})
				if err != nil {
					t.Fatal(err)
				}
				plain.Unhealthy()
				monitored.Healthy()
				data, err := m.metricsBytes()
				if err != nil {
					t.Fatal(err)
				}
				for _, c := range []*Component{plain, monitored} {
					want := map[string]float64{
						"syslogger_component_log_rejected_total":      0,
						"syslogger_component_log_send_failures_total": 0,
					}
					if c == monitored {
						want["syslogger_component_state"] = 0
						want["syslogger_component_timestamp_seconds"] = 0
						if stateEnabled {
							want["syslogger_component_state"] = float64(StateHealthy)
							want["syslogger_component_timestamp_seconds"] = float64(currentRecord(t, m, c.name).ObservedAt.Unix())
						}
					}
					labels := map[string]string{"project": cfg.Project, "instance_id": cfg.InstanceID, "component": c.name}
					assertComponentMetricSet(t, string(data), labels, want)
				}
				if currentRecord(t, m, plain.name).Reported != stateEnabled {
					t.Fatal("健康导出开关改变了内部状态维护")
				}
				if stateEnabled {
					labels := map[string]string{"project": cfg.Project, "instance_id": cfg.InstanceID, "component": diagnostic.ComponentName}
					record := currentRecord(t, m, diagnostic.ComponentName)
					assertComponentMetricSet(t, string(data), labels, map[string]float64{
						"syslogger_component_state":             float64(record.State),
						"syslogger_component_timestamp_seconds": float64(record.ObservedAt.Unix()),
					})
				}
			})
		}
	}
}

func TestHealthMetricsSelectionAcrossOutlets(t *testing.T) {
	captureLogs(t)
	cfg := testConfig(t)
	cfg.Registry = prometheus.NewPedanticRegistry()
	cfg.HTTP.Enabled = true
	cfg.File.Enabled = true
	m := testManager(t, cfg)
	plain, err := m.Register(ComponentConfig{Name: "plain"})
	if err != nil {
		t.Fatal(err)
	}
	monitored := testComponent(t, m, "monitored")
	plain.Unhealthy()
	monitored.Healthy()
	plain.Info("异步连接失败", nil)
	waitUntil(t, func() bool {
		_, failures := plain.logs.Counts()
		return failures == 1
	})
	if err := plain.InfoSync(context.Background(), "同步连接失败", nil); err == nil {
		t.Fatal("测试缺省 socket 意外可用")
	}
	plain.Info("异步编码拒收", Fields{"value": make(chan int)})
	if err := plain.InfoSync(nil, "同步上下文拒收", nil); err == nil {
		t.Fatal("nil 上下文未被拒收")
	}
	handler, err := m.Handler()
	if err != nil {
		t.Fatal(err)
	}
	external := promhttp.HandlerFor(m.Registry(), promhttp.HandlerOpts{})
	if err := m.StartFile(); err != nil {
		t.Fatal(err)
	}
	texts := []string{string(readSnapshot(t, cfg.File.OutputPath))}
	for _, outlet := range []http.Handler{handler, external} {
		response := httptest.NewRecorder()
		outlet.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("指标出口状态错误: %d", response.Code)
		}
		texts = append(texts, response.Body.String())
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	texts = append(texts, string(readSnapshot(t, cfg.File.OutputPath)))
	for _, text := range texts {
		labels := map[string]string{"project": cfg.Project, "instance_id": cfg.InstanceID, "component": plain.name}
		assertComponentMetricSet(t, text, labels, map[string]float64{
			"syslogger_component_log_rejected_total":      2,
			"syslogger_component_log_send_failures_total": 2,
		})
		labels["component"] = monitored.name
		assertComponentMetricSet(t, text, labels, map[string]float64{
			"syslogger_component_state":                   float64(StateHealthy),
			"syslogger_component_timestamp_seconds":       float64(currentRecord(t, m, monitored.name).ObservedAt.Unix()),
			"syslogger_component_log_rejected_total":      0,
			"syslogger_component_log_send_failures_total": 0,
		})
		if strings.Count(text, "# HELP syslogger_component_state ") != 1 {
			t.Fatal("状态说明未作为指标族 HELP 输出一次")
		}
		for _, meaning := range []string{"0=Unknown", "1=Healthy", "2=Degraded", "3=Unhealthy"} {
			if !strings.Contains(text, meaning) {
				t.Fatalf("状态 HELP 缺少数值含义: %s", meaning)
			}
		}
	}
}

func TestAllComponentHealthMetricsDisabled(t *testing.T) {
	for _, stateEnabled := range []bool{false, true} {
		cfg := testConfig(t)
		cfg.StateEnabled = stateEnabled
		cfg.Registry = prometheus.NewPedanticRegistry()
		m := testManager(t, cfg)
		if _, err := m.Register(ComponentConfig{Name: "plain"}); err != nil {
			t.Fatal(err)
		}
		families, err := m.Registry().Gather()
		if err != nil {
			t.Fatal(err)
		}
		for _, family := range families {
			if !stateEnabled && family.GetName() != "syslogger_component_log_rejected_total" && family.GetName() != "syslogger_component_log_send_failures_total" {
				t.Fatalf("导出了未启用的指标族: %s", family.GetName())
			}
		}
		data, err := m.metricsBytes()
		if err != nil {
			t.Fatal(err)
		}
		labels := map[string]string{"project": cfg.Project, "instance_id": cfg.InstanceID, "component": "plain"}
		assertComponentMetricSet(t, string(data), labels, map[string]float64{
			"syslogger_component_log_rejected_total":      0,
			"syslogger_component_log_send_failures_total": 0,
		})
		if stateEnabled {
			labels["component"] = diagnostic.ComponentName
			record := currentRecord(t, m, diagnostic.ComponentName)
			assertComponentMetricSet(t, string(data), labels, map[string]float64{
				"syslogger_component_state":             float64(record.State),
				"syslogger_component_timestamp_seconds": float64(record.ObservedAt.Unix()),
			})
		}
	}
}

func TestLogAndCatalogWithHealthMetricsDisabled(t *testing.T) {
	path := filepath.Join(socketDirectory(t), "log.sock")
	receiver := unixReceiver(t, path)
	cfg := testConfig(t)
	cfg.SyslogSocketPath = path
	cfg.Registry = prometheus.NewPedanticRegistry()
	m := catalogManager(t, cfg, preparedCatalogRoot(t))
	c, err := m.Register(ComponentConfig{Name: "plain"})
	if err != nil {
		t.Fatal(err)
	}
	c.Info("异步日志", nil)
	if !strings.Contains(string(receive(t, receiver)), `"message":"异步日志"`) {
		t.Fatal("关闭健康导出影响了异步交付")
	}
	if err := c.InfoSync(context.Background(), "同步日志", nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(receive(t, receiver)), `"message":"同步日志"`) {
		t.Fatal("关闭健康导出影响了同步交付")
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	instances, err := m.catalog.Read()
	if err != nil || len(instances) != 1 || len(instances[0].Components) != 1 || catalogLogCount(instances[0].Components[0]) != 2 {
		t.Fatalf("关闭健康导出改变了目录接收量: %+v, %v", instances, err)
	}
	if err := c.InfoSync(context.Background(), "关闭后", nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("关闭后的日志接收语义被改变: %v", err)
	}
}
