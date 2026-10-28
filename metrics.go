package syslogger

import (
	"bytes"
	"fmt"
	"time"

	"github.com/mctxyz/syslogger/internal/diagnostic"
	"github.com/mctxyz/syslogger/internal/record"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/expfmt"
)

// metricDefinitions 保持内置指标族及其采集顺序，业务指标由共享注册表独立管理。
var metricDefinitions = [...]struct {
	name      string
	help      string
	valueType prometheus.ValueType
}{
	{"syslogger_component_state", "组件健康状态：0=未知，1=健康，2=降级，3=不健康。", prometheus.GaugeValue},
	{"syslogger_component_timestamp_seconds", "当前健康记录的汇报时间，单位为 Unix 秒，尚无汇报时为 0。", prometheus.GaugeValue},
	{"syslogger_component_log_rejected_total", "入队前拒收的日志调用累计次数。", prometheus.CounterValue},
	{"syslogger_component_log_send_failures_total", "后台发送失败的日志条目累计数。", prometheus.CounterValue},
}

// componentSnapshot 是一次采集读取的组件健康事实及日志计数。
type componentSnapshot struct {
	Name         string
	Record       record.Record
	HasLogs      bool
	Rejected     uint64
	SendFailures uint64
}

// Registry 返回实际使用的注册表，业务可向其中注册官方 Collector 或交给 promhttp 使用。
// 业务 Collector 的更新和关闭由调用方管理，管理器关闭时只注销自身 Collector。
func (m *Manager) Registry() *prometheus.Registry {
	return m.registry
}

// Describe 提供四组内置指标的稳定描述符，项目和实例身份通过常量标签关联。
func (m *Manager) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range m.metricDescs {
		ch <- desc
	}
}

// Collect 从现有状态及 Counter 构造标准指标，首次采集固定业务组件集合。
func (m *Manager) Collect(ch chan<- prometheus.Metric) {
	m.mu.Lock()
	m.frozen = true
	m.mu.Unlock()
	for _, component := range m.snapshot() {
		var timestamp int64
		if component.Record.Reported {
			timestamp = component.Record.ObservedAt.Unix()
		}
		values := [...]float64{
			float64(component.Record.State),
			float64(timestamp),
			float64(component.Rejected),
			float64(component.SendFailures),
		}
		for index, definition := range metricDefinitions {
			if index >= 2 && !component.HasLogs {
				continue
			}
			ch <- prometheus.MustNewConstMetric(
				m.metricDescs[index],
				definition.valueType,
				values[index],
				component.Name,
			)
		}
	}
}

// snapshot 复制各组件的完整健康记录，指标输出及业务 Collector 在所有内部锁外执行。
func (m *Manager) snapshot() []componentSnapshot {
	m.mu.RLock()
	components := make([]*Component, 0, len(m.components))
	for _, component := range m.components {
		components = append(components, component)
	}
	var result []componentSnapshot
	if m.diagnostic != nil {
		result = append(result, componentSnapshot{
			Name:   diagnostic.ComponentName,
			Record: m.diagnostic.Current,
		})
	}
	m.mu.RUnlock()
	for _, component := range components {
		component.mu.RLock()
		item := componentSnapshot{
			Name:    component.name,
			Record:  component.healthAt(time.Now()),
			HasLogs: true,
		}
		component.mu.RUnlock()
		item.Rejected, item.SendFailures = component.logs.Counts()
		result = append(result, item)
	}
	return result
}

// metricsBytes 为 HTTP 和文件出口生成同一注册表的完整标准文本，采集失败时保留旧文件。
func (m *Manager) metricsBytes() ([]byte, error) {
	families, err := m.registry.Gather()
	var data bytes.Buffer
	if err == nil {
		for _, family := range families {
			if _, err = expfmt.MetricFamilyToText(&data, family); err != nil {
				break
			}
		}
	}
	if err != nil {
		m.metricsReporter.Printf("project=%q instance_id=%q: 指标采集或编码失败: %v", m.id.Project, m.id.InstanceID, err)
		return nil, fmt.Errorf("syslogger: 指标采集或编码失败: %w", err)
	}
	return data.Bytes(), nil
}
