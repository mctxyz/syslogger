package syslogger

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mctxyz/syslogger/internal/diagnostic"
	"github.com/mctxyz/syslogger/internal/logging"
)

func testSafeManager(t *testing.T, cfg Config) *Manager {
	t.Helper()
	// 所有构造测试都限定到独立测试 socket，避免向真实系统日志提交诊断。
	if cfg.SyslogSocketPath == "" {
		cfg.SyslogSocketPath = filepath.Join(t.TempDir(), "missing.sock")
	}
	m := SafeNew(cfg)
	if m == nil || m.components == nil || m.done == nil {
		t.Fatal("SafeNew 未返回完整初始化的管理器")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = m.Close(ctx)
	})
	return m
}

type safeSyslogRecord struct {
	Level      string         `json:"level"`
	Project    string         `json:"project"`
	InstanceID string         `json:"instance_id"`
	Component  string         `json:"component"`
	Fields     map[string]any `json:"fields"`
}

func receiveSafeLog(t *testing.T, receiver *net.UnixConn) ([]byte, safeSyslogRecord) {
	t.Helper()
	data := receive(t, receiver)
	start := bytes.IndexByte(data, '{')
	if start < 0 {
		t.Fatalf("syslog 缺少 JSON 正文：%q", data)
	}
	var record safeSyslogRecord
	if err := json.Unmarshal(data[start:], &record); err != nil {
		t.Fatal(err)
	}
	return data, record
}

func TestSafeNewDiagnosticsAndUsability(t *testing.T) {
	capture := captureLogs(t)
	path := filepath.Join(socketDirectory(t), "startup.sock")
	receiver := unixReceiver(t, path)
	cfg := Config{
		InstanceID:           "x",
		DefaultQueueCapacity: -1,
		SyslogSocketPath:     path,
		HTTP: HTTPConfig{
			Enabled: true,
		},
		File: FileConfig{
			Enabled:    true,
			OutputPath: " \t ",
		},
	}
	m := testSafeManager(t, cfg)
	if cfg.Project != "" || cfg.StateEnabled || !cfg.File.Enabled {
		t.Fatal("SafeNew 修改了调用方的配置值")
	}
	if m.id.Project != "common" || m.id.InstanceID != "common" || !m.cfg.StateEnabled || m.cfg.File.Enabled {
		t.Fatalf("管理器未使用纠正后的配置：%+v", m.cfg)
	}
	wantLevels := map[string]logging.Level{
		"File.Enabled":         logging.Error,
		"DefaultQueueCapacity": logging.Warning,
		"Project":              logging.Warning,
		"InstanceID":           logging.Warning,
		"StateEnabled":         logging.Warning,
	}
	text := capture.String()
	for field := range wantLevels {
		if !strings.Contains(text, fmt.Sprintf("field=%q", field)) {
			t.Fatalf("标准日志遗漏了初始化问题 %s：%s", field, text)
		}
	}
	seen := make(map[string]bool)
	for range wantLevels {
		data, record := receiveSafeLog(t, receiver)
		field, _ := record.Fields["field"].(string)
		level, exists := wantLevels[field]
		if !exists || seen[field] {
			t.Fatalf("结构化诊断字段不符或重复：%+v", record)
		}
		seen[field] = true
		if !bytes.HasPrefix(data, []byte(fmt.Sprintf("<%d>", 8+level))) {
			t.Fatalf("诊断未使用原生 severity：%q", data)
		}
		wantLevel := "warning"
		if level == logging.Error {
			wantLevel = "error"
		}
		if record.Level != wantLevel || record.Fields["state"] != "unhealthy" {
			t.Fatalf("结构化诊断等级或状态不符：%+v", record)
		}
		if record.Component != diagnostic.ComponentName || record.Project != "common" || record.InstanceID != "common" {
			t.Fatalf("初始化日志身份不符：%+v", record)
		}
	}
	initial := currentRecord(t, m, diagnostic.ComponentName)
	if initial.State != int(StateUnhealthy) || initial.ObservedAt.IsZero() {
		t.Fatal("配置纠正未形成管理器不健康事实")
	}
	rpc := testComponent(t, m, "rpc")
	if _, err := m.Register("rpc", 512); err == nil {
		t.Fatal("重复注册的原有错误契约被改变")
	}
	testComponent(t, m, "worker")
	if currentRecord(t, m, diagnostic.ComponentName) != initial {
		t.Fatal("注册失败或恢复覆盖了持续的配置异常")
	}
	rpc.Healthy()
	rpc.Degraded()
	rpc.Unhealthy()
	if currentRecord(t, m, "rpc").State != int(StateUnhealthy) {
		t.Fatal("SafeNew 的组件状态方法不可用")
	}
	rpc.Info("业务日志仍然可用", nil)
	_, business := receiveSafeLog(t, receiver)
	if business.Component != "rpc" || business.Level != "info" {
		t.Fatalf("业务日志被初始化诊断替代：%+v", business)
	}
	handler, err := m.Handler()
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	labels := map[string]string{
		"project":     "common",
		"instance_id": "common",
		"component":   diagnostic.ComponentName,
	}
	if response.Code != http.StatusOK || !hasMetricSample(t, response.Body.String(), "syslogger_component_state", labels, 3) {
		t.Fatalf("HTTP 指标未展示管理器异常：%s", response.Body.String())
	}
	if m.StartFile() == nil {
		t.Fatal("路径缺失的指标文件出口未停用")
	}
	if m.StartHTTP() == nil {
		t.Fatal("外部挂载模式的空监听地址未保留原有错误契约")
	}
	if m.httpPhase != outletIdle {
		t.Fatal("HTTP 启动失败后留下不完整的启动状态")
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal("重复关闭未复用正常结果")
	}
	if err := m.startupLogs.Wait(); err != nil {
		t.Fatalf("初始化日志队列未正确收尾：%v", err)
	}
	if _, err := m.Register("closed", 512); err == nil {
		t.Fatal("关闭后仍允许注册组件")
	}
	rpc.Info("closed", nil)
	if rejected, failed := rpc.logs.Counts(); rejected != 1 || failed != 0 {
		t.Fatalf("关闭后的日志计数不符：rejected=%d failed=%d", rejected, failed)
	}
	if err := m.StartHTTP(); !errors.Is(err, ErrClosed) {
		t.Fatalf("关闭后的 HTTP 错误不符：%v", err)
	}
	if err := m.StartFile(); !errors.Is(err, ErrClosed) {
		t.Fatalf("关闭后的文件指标错误不符：%v", err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatal("已有指标处理器在关闭后未正常拒绝请求")
	}
}

func TestSafeNewWorkingOutlets(t *testing.T) {
	captureLogs(t)
	path := filepath.Join(socketDirectory(t), "outlets.sock")
	receiver := unixReceiver(t, path)
	cfg := testConfig(t)
	cfg.SyslogSocketPath = path
	cfg.StateEnabled = false
	cfg.DefaultQueueCapacity = -1
	cfg.HTTP = HTTPConfig{
		Enabled:    true,
		ListenAddr: "127.0.0.1:0",
	}
	cfg.File.Enabled = true
	cfg.File.FlushInterval = -time.Second
	m := testSafeManager(t, cfg)
	if m.server != nil || m.filePhase != outletIdle {
		t.Fatal("构造时提前启动了指标出口")
	}
	if _, err := os.Stat(cfg.File.OutputPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("构造时写入了指标文件：%v", err)
	}
	rpc := testComponent(t, m, "rpc")
	rpc.Healthy()
	rpc.Info("outlets", nil)
	seenBusiness := false
	for i := 0; i < 4; i++ {
		_, record := receiveSafeLog(t, receiver)
		if record.Component == "rpc" {
			seenBusiness = true
		}
	}
	if !seenBusiness {
		t.Fatal("业务日志未在启用指标时正常交付")
	}
	handler, err := m.Handler()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.StartHTTP(); err != nil {
		t.Fatal(err)
	}
	if err := m.StartFile(); err != nil {
		t.Fatal(err)
	}
	if m.StartHTTP() == nil || m.StartFile() == nil {
		t.Fatal("重复启动未保留原有错误契约")
	}
	if m.cfg.File.FlushInterval != 10*time.Second {
		t.Fatal("指标文件刷新间隔未纠正")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	file := readSnapshot(t, cfg.File.OutputPath)
	if response.Body.String() != string(file) {
		t.Fatal("外部处理器与指标文件内容不一致")
	}
	address := m.server.Addr
	client := &http.Client{
		Timeout: 5 * time.Second,
	}
	result, err := client.Get("http://" + address + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(result.Body)
	closeErr := result.Body.Close()
	if readErr != nil || closeErr != nil || result.StatusCode != http.StatusOK {
		t.Fatalf("专用 HTTP 指标不可用：read=%v close=%v status=%d", readErr, closeErr, result.StatusCode)
	}
	if string(body) != string(file) {
		t.Fatal("专用 HTTP 指标与文件内容不一致")
	}
	labels := map[string]string{
		"project":     cfg.Project,
		"instance_id": cfg.InstanceID,
		"component":   diagnostic.ComponentName,
	}
	if !hasMetricSample(t, string(file), "syslogger_component_state", labels, 3) {
		t.Fatal("指标出口未保留初始化异常状态")
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(file, readSnapshot(t, cfg.File.OutputPath)) {
		t.Fatal("最终指标快照发生意外变化")
	}
	connection, err := net.DialTimeout("tcp", address, time.Second)
	if err == nil {
		_ = connection.Close()
		t.Fatal("关闭后专用 HTTP 监听仍然存在")
	}
}

func TestSafeNewUnavailableSyslog(t *testing.T) {
	for _, stateEnabled := range []bool{false, true} {
		t.Run(fmt.Sprint(stateEnabled), func(t *testing.T) {
			capture := captureLogs(t)
			cfg := testConfig(t)
			cfg.StateEnabled = stateEnabled
			cfg.DefaultQueueCapacity = -1
			m := testSafeManager(t, cfg)
			if !strings.Contains(capture.String(), `field="DefaultQueueCapacity"`) {
				t.Fatal("syslog 不可用时丢失了初始化诊断")
			}
			rpc := testComponent(t, m, "rpc")
			rpc.Healthy()
			rpc.Degraded()
			rpc.Unhealthy()
			if stateEnabled {
				if currentRecord(t, m, diagnostic.ComponentName).State != int(StateUnhealthy) {
					t.Fatal("syslog 不可用时丢失了健康异常")
				}
			} else if m.diagnostic != nil || rpc.current.Reported {
				t.Fatal("syslog 不可用时强制开启了状态维护")
			}
			rpc.Info("syslog unavailable", nil)
			waitUntil(t, func() bool {
				_, failed := rpc.logs.Counts()
				return failed == 1
			})
			if err := m.Close(context.Background()); err != nil {
				t.Fatalf("初始化诊断失败破坏了关闭结果：%v", err)
			}
			if !strings.Contains(capture.String(), `component="rpc"`) {
				t.Fatal("syslog 发送错误未通过独立标准日志报告")
			}
		})
	}
}

func TestSafeNewDisabledOutletsAndCancellation(t *testing.T) {
	captureLogs(t)
	m := testSafeManager(t, Config{
		File: FileConfig{
			Enabled:       true,
			FlushInterval: -time.Second,
		},
	})
	if m.cfg.StateEnabled || m.diagnostic != nil || m.cfg.File.Enabled {
		t.Fatal("停用无效指标文件后仍强制开启状态或保留文件出口")
	}
	rpc := testComponent(t, m, "rpc")
	rpc.Healthy()
	if _, err := m.Handler(); err == nil {
		t.Fatal("关闭的 HTTP 指标仍返回 Handler")
	}
	if m.StartHTTP() == nil || m.StartFile() == nil {
		t.Fatal("关闭的指标出口被意外启动")
	}
	if err := m.Close(nil); err == nil || m.closing {
		t.Fatal("非法关闭参数破坏了管理器")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.Close(ctx); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("取消关闭时返回意外错误：%v", err)
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatalf("取消后的后台收尾不可用：%v", err)
	}
}

func TestSafeNewValidConfigMatchesNew(t *testing.T) {
	for _, stateEnabled := range []bool{false, true} {
		t.Run(fmt.Sprint(stateEnabled), func(t *testing.T) {
			capture := captureLogs(t)
			cfg := Config{
				Project:          " 项目 A ",
				StateEnabled:     stateEnabled,
				SyslogSocketPath: filepath.Join(t.TempDir(), "missing.sock"),
			}
			strict := testManager(t, cfg)
			safe := testSafeManager(t, cfg)
			if !reflect.DeepEqual(strict.cfg, safe.cfg) || strict.id != safe.id {
				t.Fatal("合法配置与严格构造的结果不一致")
			}
			if safe.startupLogs != nil || capture.String() != "" {
				t.Fatal("正常默认值触发了初始化告警")
			}
			testComponent(t, safe, "rpc")
			if stateEnabled && currentRecord(t, safe, diagnostic.ComponentName).State != int(StateHealthy) {
				t.Fatal("正常初始化未保持健康状态")
			}
		})
	}
}
