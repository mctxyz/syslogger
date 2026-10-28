package syslogger_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"

	"github.com/mctxyz/syslogger"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func ExampleComponent_Unhealthy() {
	manager, err := syslogger.New(syslogger.Config{Project: "demo", StateEnabled: true, HTTP: syslogger.HTTPConfig{Enabled: true}})
	if err != nil {
		panic(err)
	}
	defer manager.Close(context.Background())
	rpc, err := manager.Register(syslogger.ComponentConfig{Name: "rpc", QueueCapacity: 512})
	if err != nil {
		panic(err)
	}
	rpc.Unhealthy()
	handler, err := manager.Handler()
	if err != nil {
		panic(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	fmt.Println(strings.Contains(response.Body.String(), `syslogger_component_state{component="rpc",instance_id="common",project="demo"} 3`))
	// Output: true
}

func ExampleSafeNew() {
	manager := syslogger.SafeNew(syslogger.Config{
		Project:      "demo",
		StateEnabled: true,
		HTTP: syslogger.HTTPConfig{
			Enabled: true,
		},
	})
	defer manager.Close(context.Background())
	rpc, err := manager.Register(syslogger.ComponentConfig{Name: "rpc", QueueCapacity: 512})
	if err != nil {
		panic(err)
	}
	rpc.Healthy()
	handler, err := manager.Handler()
	if err != nil {
		panic(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	containsState := strings.Contains(
		response.Body.String(),
		`syslogger_component_state{component="rpc",instance_id="common",project="demo"} 1`,
	)
	fmt.Println(containsState)
	// Output: true
}

// ExampleManager_Registry 展示业务指标与组件健康共用已有 promhttp 出口。
func ExampleManager_Registry() {
	manager, err := syslogger.New(syslogger.Config{
		Project:      "demo",
		StateEnabled: true,
	})
	if err != nil {
		panic(err)
	}
	defer manager.Close(context.Background())
	rpc, err := manager.Register(syslogger.ComponentConfig{Name: "rpc", QueueCapacity: 512})
	if err != nil {
		panic(err)
	}
	rpc.Healthy()
	height := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "business_node_height",
		Help: "节点高度",
	}, []string{"chain"})
	manager.Registry().MustRegister(height)
	height.WithLabelValues("eth").Set(123)
	handler := promhttp.HandlerFor(manager.Registry(), promhttp.HandlerOpts{})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	text := response.Body.String()
	fmt.Println(strings.Contains(text, `business_node_height{chain="eth"} 123`))
	fmt.Println(strings.Contains(text, `syslogger_component_state{component="rpc",instance_id="common",project="demo"} 1`))
	// Output:
	// true
	// true
}
