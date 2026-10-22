package syslogger

import (
	"fmt"
	"time"

	"github.com/mctxyz/syslogger/internal/metrics"
	"github.com/mctxyz/syslogger/internal/textfile"
)

// StartFile 同步写入首次完整快照，成功后启动固定周期的文件交付。
func (m *Manager) StartFile() error {
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return ErrClosed
	}
	if !m.cfg.File.Enabled {
		m.mu.Unlock()
		return fmt.Errorf("syslogger: 文件指标未启用")
	}
	if m.filePhase != outletIdle {
		m.mu.Unlock()
		return fmt.Errorf("syslogger: 文件出口已启动或正在启动")
	}
	m.frozen = true
	m.filePhase = outletStarting
	m.starters.Add(1)
	m.mu.Unlock()
	defer m.starters.Done()
	err := m.flush()
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.filePhase = outletIdle
		return err
	}
	if m.closing {
		m.filePhase = outletIdle
		return ErrClosed
	}
	m.fileStop = make(chan struct{})
	m.fileDone = make(chan struct{})
	m.filePhase = outletRunning
	go m.runFile()
	return nil
}

func (m *Manager) flush() error {
	return textfile.Write(m.cfg.File.OutputDir, textfile.Filename(m.id), metrics.Encode(m.id, m.snapshot()))
}

func (m *Manager) runFile() {
	defer close(m.fileDone)
	ticker := time.NewTicker(m.cfg.File.FlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.fileStop:
			return
		default:
		}
		select {
		case <-m.fileStop:
			return
		case <-ticker.C:
			_ = m.flush()
		}
	}
}
