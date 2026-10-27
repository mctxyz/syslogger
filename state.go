// Package syslogger 提供本机 syslog 异步交付和组件健康指标。
package syslogger

import (
	"time"

	"github.com/mctxyz/syslogger/internal/record"
)

// State 是组件当前健康结论，同时作为状态 Gauge 的数值。
type State int

const (
	// StateUnknown 表示组件尚未形成健康报告。
	StateUnknown State = iota
	// StateHealthy 表示能够正常履行职责。
	StateHealthy
	// StateDegraded 表示部分能力受损。
	StateDegraded
	// StateUnhealthy 表示无法正常履行职责。
	StateUnhealthy
)

// Healthy 同步汇报业务正常，启用队满自动降级时有效状态同时取决于恢复窗口。
// 状态维护关闭或管理器关闭后安全忽略。
func (c *Component) Healthy() {
	c.setState(StateHealthy)
}

// Degraded 同步汇报业务部分能力受损，业务主动故障持续由业务恢复。
func (c *Component) Degraded() {
	c.setState(StateDegraded)
}

// Unhealthy 同步汇报业务无法正常履行职责，业务主动故障持续由业务恢复。
func (c *Component) Unhealthy() {
	c.setState(StateUnhealthy)
}

func (c *Component) setState(state State) {
	if c == nil || c.manager == nil || !c.manager.cfg.StateEnabled {
		return
	}
	at := time.Now()
	m := c.manager
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closing {
		return
	}
	c.mu.Lock()
	c.current.Apply(record.New(int(state), at))
	c.mu.Unlock()
}

// healthAt 在持有组件读锁或写锁时合成有效状态，业务主动故障优先。
// 窗口恢复由采集时间自然推导，复用现有快照流程；关闭流程冻结同一规则的结果。
func (c *Component) healthAt(at time.Time) record.Record {
	current := c.current
	if !c.manager.cfg.DegradeOnQueueFull || c.lastQueueFull.IsZero() {
		return current
	}
	if current.State >= int(StateDegraded) {
		return current
	}
	recoveredAt := c.lastQueueFull.Add(c.manager.cfg.QueueFullRecoveryInterval)
	if at.Before(recoveredAt) {
		return record.New(int(StateDegraded), c.lastQueueFull)
	}
	current.Apply(record.New(int(StateHealthy), recoveredAt))
	return current
}
