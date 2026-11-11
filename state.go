// Package syslogger 提供本机 syslog 异步交付和组件健康指标。
package syslogger

import (
	"fmt"
	"log"
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

// overallHealthName 标识 SDK 派生的组件整体，业务名称不能占用它。
const overallHealthName = "overall"

// Healthy 同步汇报指定名称的业务正常；同名报告更新同一条状态及时间。
// 非法名称拒绝写入并独立诊断，合法报告在状态维护关闭或管理器关闭后安全忽略。
func (c *Component) Healthy(name string) {
	c.setState(name, StateHealthy)
}

// Degraded 同步汇报指定名称的部分能力受损，仅由该名称后续报告恢复。
func (c *Component) Degraded(name string) {
	c.setState(name, StateDegraded)
}

// Unhealthy 同步汇报指定名称无法正常履行职责，不覆盖其他名称的记录。
func (c *Component) Unhealthy(name string) {
	c.setState(name, StateUnhealthy)
}

func (c *Component) setState(name string, state State) {
	err := validateName(name)
	if err == nil && name == overallHealthName {
		err = fmt.Errorf("健康名称 %q 用于组件整体汇总", name)
	}
	if err != nil {
		// 参数诊断在状态锁外执行；有效组件独立限流，保留日志交付诊断的输出机会。
		if c == nil || c.manager == nil {
			log.Printf("syslogger: 健康报告已拒绝: name=%q: %v", name, err)
		} else {
			c.healthReporter.Printf(
				"project=%q instance_id=%q component=%q: 健康报告已拒绝: name=%q: %v",
				c.manager.id.Project, c.manager.id.InstanceID, c.name, name, err,
			)
		}
		return
	}
	if c == nil || c.manager == nil || !c.manager.cfg.StateEnabled {
		return
	}
	at := time.Now()
	m := c.manager
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.closedAt.IsZero() {
		return
	}
	c.mu.Lock()
	if c.health == nil {
		c.health = make(map[string]record.Record)
	}
	current := c.health[name]
	current.Apply(record.New(int(state), at))
	c.health[name] = current
	c.mu.Unlock()
}

// healthAt 在组件锁内汇总业务记录，再合成组件队满状态；at 由管理器统一冻结。
func (c *Component) healthAt(at time.Time) record.Record {
	var current record.Record
	healthy, unhealthy := 0, 0
	for _, business := range c.health {
		current.Apply(business)
		switch State(business.State) {
		case StateHealthy:
			healthy++
		case StateUnhealthy:
			unhealthy++
		}
	}
	if len(c.health) > 0 {
		switch {
		case healthy == len(c.health):
			current.State = int(StateHealthy)
		case unhealthy == len(c.health):
			current.State = int(StateUnhealthy)
		default:
			current.State = int(StateDegraded)
		}
	}
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
	// 没有业务报告时，日志能力恢复不能代替业务得出 Healthy 结论。
	if current.Reported {
		current.Apply(record.New(int(StateHealthy), recoveredAt))
	}
	return current
}
