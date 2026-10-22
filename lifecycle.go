package syslogger

import (
	"context"
	"errors"
	"strings"
)

// Close 建立统一接收截止，以首次 ctx 限定日志排空，后续调用复用同次收尾。
// 文件系统及独立诊断输出的底层 I/O 可能继续，后续 ctx 只限定本次等待。
func (m *Manager) Close(ctx context.Context) error {
	if ctx == nil {
		return errors.New("syslogger: Close 需要非 nil 上下文")
	}
	m.mu.Lock()
	if !m.closing {
		m.closing = true
		components := make([]*Component, 0, len(m.components))
		for _, component := range m.components {
			component.logs.Stop(ctx)
			components = append(components, component)
		}
		if m.filePhase == outletRunning {
			close(m.fileStop)
		}
		go m.finish(ctx, components)
	}
	m.mu.Unlock()
	select {
	case <-m.done:
		return m.finalErr
	default:
	}
	select {
	case <-m.done:
		return m.finalErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) finish(ctx context.Context, components []*Component) {
	// 启动资格在集合锁内登记；截止后等待已有初始化完成并自行清理。
	m.starters.Wait()
	var errs []error
	if m.server != nil {
		if err := m.server.Shutdown(ctx); err != nil {
			errs = append(errs, err)
			_ = m.server.Close()
		}
		<-m.httpDone
	}
	for _, component := range components {
		if err := component.logs.Wait(); err != nil {
			errs = append(errs, err)
		}
	}
	if m.filePhase == outletRunning {
		<-m.fileDone
		// 周期写入和发送任务均已结束，最终快照最后可见。
		if err := m.flush(); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) == 1 {
		m.finalErr = errs[0]
	} else if len(errs) > 1 {
		m.finalErr = &closeErrors{errors: errs}
	}
	close(m.done)
}

// Go 1.19 下仍保留多个收尾错误的 errors.Is 和 errors.As 可识别性。
type closeErrors struct{ errors []error }

func (e *closeErrors) Error() string {
	parts := make([]string, len(e.errors))
	for i, err := range e.errors {
		parts[i] = err.Error()
	}
	return strings.Join(parts, "; ")
}
func (e *closeErrors) Is(target error) bool {
	for _, err := range e.errors {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}
func (e *closeErrors) As(target any) bool {
	for _, err := range e.errors {
		if errors.As(err, target) {
			return true
		}
	}
	return false
}
