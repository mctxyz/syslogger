package syslogger

import (
	"log"
	"sync"
	"time"

	"github.com/mctxyz/syslogger/internal/diagnostic"
	"github.com/mctxyz/syslogger/internal/identity"
	"github.com/mctxyz/syslogger/internal/logging"
	"github.com/mctxyz/syslogger/internal/record"
)

// Component 绑定固定模块身份、独立日志队列和同步健康记录，可并发使用。
type Component struct {
	manager     *Manager
	name        string
	description string
	logs        *logging.Queue
	logReporter diagnostic.Reporter
	mu          sync.RWMutex
	// current 保存业务主动记录，关闭后保存截止时冻结的有效健康记录。
	current record.Record
	// lastQueueFull 由真实的队满拒收更新，以组件锁保护恢复窗口的唯一时间依据。
	lastQueueFull time.Time
}

func (c *Component) log(level logging.Level, message string, fields Fields) {
	if c == nil || c.manager == nil {
		name := ""
		if c != nil {
			name = c.name
		}
		entry, err := logging.Encode(identity.Instance{}, name, level, message, fields, time.Now())
		warning := "syslogger: 无效组件句柄，日志已拒收"
		if err != nil {
			log.Printf("%s\n%v", warning, err)
			return
		}
		// 缺少管理器时使用空实例身份，警告和事件通过同一次标准日志输出保持相邻。
		log.Printf("%s\n%s", warning, entry.Body)
		return
	}
	m := c.manager
	entry, err := logging.Encode(m.id, c.name, level, message, fields, time.Now())
	if err != nil {
		c.logs.Reject()
	} else {
		m.mu.RLock()
		if m.closing {
			c.logs.Reject()
			err = ErrClosed
		} else {
			err = c.logs.Enqueue(entry)
			if err == logging.ErrQueueFull && m.cfg.DegradeOnQueueFull {
				c.mu.Lock()
				c.lastQueueFull = time.Now()
				c.mu.Unlock()
			}
		}
		m.mu.RUnlock()
	}
	if err != nil {
		// 标准日志的输出由接入方配置，I/O 在生命周期锁外执行并按模块限流。
		c.logReporter.Printf(
			"project=%q instance_id=%q component=%q: 日志提交失败，本条日志已拒收: %v",
			m.id.Project,
			m.id.InstanceID,
			c.name,
			err,
		)
	}
}
