package syslogger

import (
	"sync"
	"time"

	"github.com/mctxyz/syslogger/internal/logging"
	"github.com/mctxyz/syslogger/internal/record"
)

// Component 绑定固定模块身份、独立日志队列和同步健康记录，可并发使用。
type Component struct {
	manager *Manager
	name    string
	logs    *logging.Queue
	mu      sync.RWMutex
	current record.Record
}

// Healthy 同步汇报正常状态，状态维护关闭或管理器关闭后安全忽略。
func (c *Component) Healthy() { c.setState(StateHealthy) }

// Degraded 同步汇报部分能力受损的状态。
func (c *Component) Degraded() { c.setState(StateDegraded) }

// Unhealthy 同步汇报无法正常履行职责的状态。
func (c *Component) Unhealthy() { c.setState(StateUnhealthy) }

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

func (c *Component) log(level logging.Level, message string, fields map[string]any) error {
	if c == nil || c.manager == nil {
		return ErrClosed
	}
	m := c.manager
	entry, err := logging.Encode(m.id, c.name, level, message, fields, time.Now())
	if err != nil {
		c.logs.Reject()
		return err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closing {
		c.logs.Reject()
		return ErrClosed
	}
	return c.logs.Enqueue(entry)
}
