package syslogger

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func TestHealthLabelOrderAcrossSDKOutlets(t *testing.T) {
	cfg := testConfig(t)
	cfg.HTTP.Enabled = true
	cfg.File.Enabled = true
	cfg.Registry = prometheus.NewPedanticRegistry()
	m := testManager(t, cfg)
	c := testComponent(t, m, "rpc")
	c.Healthy("connection")
	c.Unhealthy("storage")
	c.mu.RLock()
	connectionTimestamp := c.health["connection"].ObservedAt.Unix()
	c.mu.RUnlock()
	business := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "business_status",
		Help: "业务状态",
	}, []string{"project", "instance_id", "component", "name"})
	m.Registry().MustRegister(business)
	business.WithLabelValues(cfg.Project, cfg.InstanceID, "business", "connection").Set(7)
	handler, err := m.Handler()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.StartFile(); err != nil {
		t.Fatal(err)
	}
	texts := []string{string(readSnapshot(t, cfg.File.OutputPath))}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("SDK HTTP 指标请求失败: %d", response.Code)
	}
	texts = append(texts, response.Body.String())
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	texts = append(texts, string(readSnapshot(t, cfg.File.OutputPath)))
	for _, text := range texts {
		for _, metric := range []string{"syslogger_component_state", "syslogger_component_timestamp_seconds"} {
			for _, name := range []string{"connection", "storage", "overall"} {
				prefix := fmt.Sprintf(`%s{name=%q,component="rpc",instance_id=%q,project=%q} `, metric, name, cfg.InstanceID, cfg.Project)
				if strings.Count(text, prefix) != 1 {
					t.Fatalf("健康标签展示顺序不符: %s\n%s", prefix, text)
				}
			}
			prefix := fmt.Sprintf(`%s{name="overall",component="syslogger_manager",instance_id=%q,project=%q} `, metric, cfg.InstanceID, cfg.Project)
			if strings.Count(text, prefix) != 1 {
				t.Fatalf("管理器健康标签顺序不符: %s", prefix)
			}
		}
		for _, metric := range []string{"syslogger_component_log_rejected_total", "syslogger_component_log_send_failures_total"} {
			prefix := fmt.Sprintf(`%s{component="rpc",instance_id=%q,project=%q} 0`, metric, cfg.InstanceID, cfg.Project)
			if strings.Count(text, prefix) != 1 {
				t.Fatalf("日志 Counter 的标签或数值被改变: %s", prefix)
			}
		}
		prefix := fmt.Sprintf(`business_status{component="business",instance_id=%q,name="connection",project=%q} 7`, cfg.InstanceID, cfg.Project)
		if strings.Count(text, prefix) != 1 {
			t.Fatal("外部业务指标的标签顺序或数值被改变")
		}
		assertComponentMetricSet(t, text, map[string]string{
			"project": cfg.Project, "instance_id": cfg.InstanceID, "component": "rpc", "name": "connection",
		}, map[string]float64{
			"syslogger_component_state":             float64(StateHealthy),
			"syslogger_component_timestamp_seconds": float64(connectionTimestamp),
		})
	}
}

func TestHealthLabelFormattingPreservesSharedCollectorLabels(t *testing.T) {
	cfg := testConfig(t)
	cfg.Registry = prometheus.NewPedanticRegistry()
	m := testManager(t, cfg)
	testComponent(t, m, "rpc").Healthy("service")
	// 重复交付同一个官方 Metric，模拟业务 Collector 跨采集复用标签数组。
	desc := prometheus.NewDesc(metricDefinitions[0].name, metricDefinitions[0].help,
		[]string{"component", "name"}, prometheus.Labels{"project": "shared", "instance_id": "external"})
	cached := prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, 3, "cached", "connection")
	m.Registry().MustRegister(&callbackCollector{
		desc: desc,
		collect: func(ch chan<- prometheus.Metric) {
			ch <- cached
		},
	})
	families, err := m.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	external := promhttp.HandlerFor(m.Registry(), promhttp.HandlerOpts{})
	for worker := 0; worker < 4; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for attempt := 0; attempt < 20; attempt++ {
				data, err := m.metricsBytes()
				if err != nil {
					t.Error(err)
					return
				}
				if !strings.Contains(string(data), `syslogger_component_state{name="connection",component="cached",instance_id="external",project="shared"} 3`) {
					t.Error("共享指标未按 SDK 文本顺序展示")
					return
				}
				response := httptest.NewRecorder()
				external.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
				if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `syslogger_component_state{component="cached",instance_id="external",name="connection",project="shared"} 3`) {
					t.Error("SDK 格式化改变了外部 promhttp 的标签顺序或数值")
					return
				}
			}
		}()
	}
	workers.Wait()
	for _, family := range families {
		for _, metric := range family.Metric {
			var names []string
			for _, label := range metric.Label {
				names = append(names, label.GetName())
			}
			want := []string{"component", "instance_id", "name", "project"}
			if strings.HasSuffix(family.GetName(), "_total") {
				want = []string{"component", "instance_id", "project"}
			}
			if !reflect.DeepEqual(names, want) {
				t.Fatalf("格式化覆盖了先前采集结果的标签数组: got=%v want=%v", names, want)
			}
		}
	}
}
