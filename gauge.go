package syslogger

import (
	"fmt"
	"slices"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

// Labels 保存一次数量报告的业务标签值，内置身份由注册后的 Gauge 固定。
type Labels map[string]string

// GaugeConfig 定义组件数量指标的测量对象和业务分类，注册后固定。
type GaugeConfig struct {
	// Metric 是指标后缀，SDK 自动添加 syslogger_component_ 前缀。
	Metric string
	// Name 是该指标下的逻辑对象名，作为内置 name 标签值。
	Name string
	// Help 描述所测数量，同一完整指标名必须采用相同说明。
	Help string
	// LabelNames 声明可选业务标签，同一完整指标名须保持标签名集合一致。
	LabelNames []string
}

// Gauge 绑定组件及逻辑对象身份，通过官方 GaugeVec 保存当前值，可并发更新。
type Gauge struct {
	manager *Manager
	vector  *prometheus.GaugeVec
}

// RegisterGauge 在指标出口启动或首次采集前注册数量指标，由 Manager 统一关闭。
// 注册资格在管理器锁内裁决，已获准的注册可在集合冻结后完成。
// 注册冲突直接返回错误，现有指标及组件健康、日志计数保持原状。
func (c *Component) RegisterGauge(cfg GaugeConfig) (*Gauge, error) {
	if c == nil || c.manager == nil {
		return nil, fmt.Errorf("syslogger: 无效组件句柄")
	}
	m := c.manager
	m.mu.Lock()
	if !m.closedAt.IsZero() {
		m.mu.Unlock()
		return nil, ErrClosed
	}
	if m.frozen {
		m.mu.Unlock()
		return nil, fmt.Errorf("syslogger: 当前生命周期阶段无法注册 Gauge")
	}
	// 沿用出口初始化的等待机制，Registry 的业务描述回调在管理器锁外运行。
	m.starters.Add(1)
	m.mu.Unlock()
	defer m.starters.Done()
	if strings.HasPrefix(cfg.Metric, "syslogger_component_") {
		return nil, fmt.Errorf("syslogger: Gauge Metric 只接收指标后缀: %q", cfg.Metric)
	}
	if err := validateName(cfg.Name); err != nil {
		return nil, fmt.Errorf("syslogger: Gauge Name: %w", err)
	}
	fullName := prometheus.BuildFQName("syslogger", "component", cfg.Metric)
	for _, definition := range metricDefinitions {
		if definition.name == fullName {
			return nil, fmt.Errorf("syslogger: Gauge 指标名称为内置指标保留: %q", fullName)
		}
	}
	for _, label := range cfg.LabelNames {
		switch label {
		case "project", "instance_id", "component", "name":
			return nil, fmt.Errorf("syslogger: Gauge 标签名称为内置身份保留: %q", label)
		}
		if strings.HasPrefix(label, "__") {
			return nil, fmt.Errorf("syslogger: Gauge 标签名称的 __ 前缀为 Prometheus 保留: %q", label)
		}
	}
	vector := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "syslogger",
		Subsystem: "component",
		Name:      cfg.Metric,
		Help:      cfg.Help,
		ConstLabels: prometheus.Labels{
			"project":     m.id.Project,
			"instance_id": m.id.InstanceID,
			"component":   c.name,
			"name":        cfg.Name,
		},
	}, slices.Clone(cfg.LabelNames))
	if err := m.registry.Register(vector); err != nil {
		return nil, fmt.Errorf("syslogger: 注册 Gauge %q: %w", fullName, err)
	}
	m.mu.Lock()
	if !m.closedAt.IsZero() {
		m.mu.Unlock()
		m.registry.Unregister(vector)
		return nil, ErrClosed
	}
	m.gauges = append(m.gauges, vector)
	m.mu.Unlock()
	return &Gauge{manager: m, vector: vector}, nil
}

// Set 同步覆盖当前值，labels 可省略或提供一份与注册定义匹配的业务标签。
// 调用期间由业务保证标签 map 无并发修改，返回后修改不影响已报告样本。
// 首次成功报告才创建样本；首次 Close 截止后返回 ErrClosed，不改变最终值。
func (g *Gauge) Set(value float64, labels ...Labels) error {
	if g == nil || g.manager == nil {
		return fmt.Errorf("syslogger: 无效 Gauge 句柄")
	}
	m := g.manager
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.closedAt.IsZero() {
		return ErrClosed
	}
	if len(labels) > 1 {
		return fmt.Errorf("syslogger: Gauge Set 最多接收一份业务标签")
	}
	var values prometheus.Labels
	if len(labels) == 1 {
		values = prometheus.Labels(labels[0])
	}
	metric, err := g.vector.GetMetricWith(values)
	if err != nil {
		return fmt.Errorf("syslogger: Gauge 业务标签不匹配: %w", err)
	}
	metric.Set(value)
	return nil
}
