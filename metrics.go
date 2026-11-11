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
	name string
	help string
}{
	{"syslogger_component_state", "具名业务及组件整体的健康状态：0=Unknown（尚无健康报告），1=Healthy（正常），2=Degraded（部分能力受损），3=Unhealthy（无法正常履行职责）。"},
	{"syslogger_component_timestamp_seconds", "当前健康记录的汇报时间，单位为 Unix 秒，尚无汇报时为 0。"},
	{"syslogger_component_log_rejected_total", "接收前拒收的日志调用累计次数。"},
	{"syslogger_component_log_send_failures_total", "异步及同步发送失败的日志条目累计数。"},
}

// componentSnapshot 是一次采集读取的组件健康事实及日志计数。
type componentSnapshot struct {
	Name string
	// Health 含具名业务记录及同次快照派生的整体，不与组件日志计数交叉展开。
	Health map[string]record.Record
	// HealthMetrics 选择导出健康样本，内部记录仍供状态检查使用。
	HealthMetrics bool
	HasLogs       bool
	Rejected      uint64
	SendFailures  uint64
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
		if component.HealthMetrics {
			for name, health := range component.Health {
				var timestamp int64
				if health.Reported {
					timestamp = health.ObservedAt.Unix()
				}
				values := [...]float64{float64(health.State), float64(timestamp)}
				for index, value := range values {
					ch <- prometheus.MustNewConstMetric(
						m.metricDescs[index], prometheus.GaugeValue, value, component.Name, name,
					)
				}
			}
		}
		if component.HasLogs {
			values := [...]float64{float64(component.Rejected), float64(component.SendFailures)}
			for index, value := range values {
				ch <- prometheus.MustNewConstMetric(
					m.metricDescs[index+2], prometheus.CounterValue, value, component.Name,
				)
			}
		}
	}
}

// snapshot 复制各组件的完整健康记录，指标输出及业务 Collector 在所有内部锁外执行。
func (m *Manager) snapshot() []componentSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	at := m.closedAt
	if at.IsZero() {
		at = time.Now()
	}
	var result []componentSnapshot
	if m.diagnostic != nil {
		result = append(result, componentSnapshot{
			Name:          diagnostic.ComponentName,
			Health:        map[string]record.Record{overallHealthName: m.diagnostic.Current},
			HealthMetrics: true,
		})
	}
	for _, component := range m.components {
		component.mu.RLock()
		health := make(map[string]record.Record, len(component.health)+1)
		for name, business := range component.health {
			health[name] = business
		}
		health[overallHealthName] = component.healthAt(at)
		item := componentSnapshot{
			Name:          component.name,
			Health:        health,
			HealthMetrics: component.enableHealthMetrics,
			HasLogs:       true,
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
