package syslogger

import (
	"context"
	"errors"
	"time"

	"github.com/mctxyz/syslogger/internal/logging"
)

// EmergencySync 同步发送 severity 0 事件，返回本机 socket 的交付结果，进程退出由业务决定。
func (c *Component) EmergencySync(ctx context.Context, message string, fields Fields) error {
	return c.logSync(ctx, logging.Emergency, message, fields)
}

// AlertSync 同步发送 severity 1 事件，ctx 可取消连接及写入等待。
func (c *Component) AlertSync(ctx context.Context, message string, fields Fields) error {
	return c.logSync(ctx, logging.Alert, message, fields)
}

// CriticalSync 同步发送 severity 2 事件，使用独立连接直接交付本机日志服务。
func (c *Component) CriticalSync(ctx context.Context, message string, fields Fields) error {
	return c.logSync(ctx, logging.Critical, message, fields)
}

// ErrorSync 同步发送 severity 3 事件，错误直接返回，组件健康状态由业务单独维护。
func (c *Component) ErrorSync(ctx context.Context, message string, fields Fields) error {
	return c.logSync(ctx, logging.Error, message, fields)
}

// WarningSync 同步发送 severity 4 事件，发送失败后由调用方处理错误。
func (c *Component) WarningSync(ctx context.Context, message string, fields Fields) error {
	return c.logSync(ctx, logging.Warning, message, fields)
}

// NoticeSync 同步发送 severity 5 事件，成功表示已写入本机 socket。
func (c *Component) NoticeSync(ctx context.Context, message string, fields Fields) error {
	return c.logSync(ctx, logging.Notice, message, fields)
}

// InfoSync 同步发送 severity 6 事件，与普通队列分别交付。
func (c *Component) InfoSync(ctx context.Context, message string, fields Fields) error {
	return c.logSync(ctx, logging.Info, message, fields)
}

// DebugSync 同步发送 severity 7 事件，日志服务的过滤及持久化策略由设备管理。
func (c *Component) DebugSync(ctx context.Context, message string, fields Fields) error {
	return c.logSync(ctx, logging.Debug, message, fields)
}

func (c *Component) logSync(ctx context.Context, level logging.Level, message string, fields Fields) error {
	if c == nil || c.manager == nil {
		return errors.New("syslogger: 无效组件句柄")
	}
	if ctx == nil {
		c.logs.Reject()
		return errors.New("syslogger: 同步日志需要非 nil 上下文")
	}
	if err := ctx.Err(); err != nil {
		c.logs.Reject()
		return err
	}
	m := c.manager
	entry, err := logging.Encode(m.id, c.name, level, message, fields, time.Now())
	if err != nil {
		c.logs.Reject()
		return err
	}
	m.mu.RLock()
	closing := m.closing
	m.mu.RUnlock()
	if closing {
		c.logs.Reject()
		return ErrClosed
	}
	// 接收与 Close 的竞争由队列锁裁决，网络 I/O 保持在管理器锁外。
	return c.logs.SendSync(ctx, entry)
}
