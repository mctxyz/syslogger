package syslogger

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestExternalHandlerAndFixedCollection(t *testing.T) {
	cfg := testConfig(t)
	cfg.HTTP.Enabled = true
	m := testManager(t, cfg)
	c := testComponent(t, m, "rpc")
	c.Unhealthy()
	handler, err := m.Handler()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Register("late", 1); err == nil {
		t.Fatal("Handler 后仍允许注册")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/custom", nil))
	text := response.Body.String()
	if response.Code != 200 || !strings.Contains(response.Header().Get("Content-Type"), "version=0.0.4") || !strings.Contains(text, `syslogger_component_state{project="demo",instance_id="instance-a",component="rpc"} 3`) {
		t.Fatalf("指标响应不符: %s", text)
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/custom", nil))
	if response.Code != 503 {
		t.Fatal("关闭后的 Handler 未返回 503")
	}
}

func TestDedicatedListenerBindRetryAndOwnership(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	cfg := testConfig(t)
	cfg.HTTP = HTTPConfig{Enabled: true, ListenAddr: occupied.Addr().String()}
	m := testManager(t, cfg)
	testComponent(t, m, "rpc")
	if err := m.StartHTTP(); err == nil {
		t.Fatal("占用端口未返回启动错误")
	}
	if m.httpPhase != outletIdle {
		t.Fatal("失败未复原启动资格")
	}
	_ = occupied.Close()
	if err := m.StartHTTP(); err != nil {
		t.Fatal(err)
	}
	if err := m.StartHTTP(); err == nil {
		t.Fatal("重复监听未失败")
	}
	response, err := http.Get("http://" + cfg.HTTP.ListenAddr + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("专用路由不可用")
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", cfg.HTTP.ListenAddr)
	if err == nil {
		_ = conn.Close()
		t.Fatal("Close 后监听资源残留")
	}
}

func TestDisabledOutlets(t *testing.T) {
	m := testManager(t, Config{Project: "demo"})
	if _, err := m.Handler(); err == nil {
		t.Fatal("HTTP 关闭仍返回 Handler")
	}
	if m.StartHTTP() == nil || m.StartFile() == nil {
		t.Fatal("关闭的出口被启动")
	}
}

func TestCommonIdentityAcrossLogsHTTPAndFile(t *testing.T) {
	path := filepath.Join(socketDirectory(t), "log.sock")
	receiver := unixReceiver(t, path)
	cfg := testConfig(t)
	cfg.InstanceID = ""
	cfg.SyslogSocketPath = path
	cfg.HTTP.Enabled = true
	cfg.File.Enabled = true
	m := testManager(t, cfg)
	c := testComponent(t, m, "rpc")
	c.Healthy()
	if err := c.Info("默认身份", nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(receive(t, receiver)), `"instance_id":"common"`) {
		t.Fatal("日志未使用默认实例身份")
	}
	handler, err := m.Handler()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.StartFile(); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	if response.Body.String() != string(readSnapshot(t, cfg.File.OutputPath)) {
		t.Fatal("HTTP 和文件没有使用同一指标契约")
	}
	if !strings.Contains(response.Body.String(), `instance_id="common"`) {
		t.Fatal("指标未使用默认实例身份")
	}
}
