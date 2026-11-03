package syslogger

import (
	"time"

	"github.com/mctxyz/syslogger/internal/catalog"
	"github.com/mctxyz/syslogger/internal/diagnostic"
	"github.com/mctxyz/syslogger/internal/logging"
)

func (m *Manager) startCatalog() {
	if m.catalog != nil {
		go m.runCatalog()
	}
}

func (m *Manager) runCatalog() {
	defer close(m.catalogDone)
	defer m.publishCatalog()
	ticker := time.NewTicker(catalog.SyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.catalogWake:
			m.publishCatalog()
		case <-ticker.C:
			m.publishCatalog()
			if err := m.catalog.Prune(time.Now().UTC()); err != nil {
				m.catalogReporter.Printf("project=%q instance_id=%q: 清理过期日志名称目录失败: %v", m.id.Project, m.id.InstanceID, err)
			}
		case <-m.catalogStop:
			return
		}
	}
}

// publishCatalog 取走组件入队窗口后在集合锁外合并写入，失败时恢复窗口供下周期同步。
func (m *Manager) publishCatalog() {
	if m.catalog == nil {
		return
	}
	m.catalogMu.Lock()
	defer m.catalogMu.Unlock()
	m.mu.RLock()
	instance := catalog.Instance{
		Project:             m.id.Project,
		InstanceID:          m.id.InstanceID,
		InstanceDescription: m.cfg.InstanceDescription,
		Components:          make([]catalog.Component, 0, len(m.components)+1),
	}
	type window struct {
		queue    *logging.Queue
		activity logging.Activity
	}
	var windows []window
	collect := func(queue *logging.Queue, name, description string) {
		activity := queue.TakeActivity()
		if activity.FirstAt.IsZero() {
			return
		}
		windows = append(windows, window{queue: queue, activity: activity})
		instance.Components = append(instance.Components, catalog.Component{
			Name:         name,
			Description:  description,
			CreatedAt:    activity.FirstAt,
			LastActiveAt: activity.LastAt,
			LogCount:     activity.Count,
		})
	}
	if m.startupLogs != nil {
		collect(m.startupLogs, diagnostic.ComponentName, "管理器初始化与组件注册诊断")
	}
	for _, component := range m.components {
		collect(component.logs, component.name, component.description)
	}
	m.mu.RUnlock()
	if len(windows) == 0 {
		return
	}
	if err := m.catalog.Merge(instance, time.Now().UTC()); err != nil {
		for _, window := range windows {
			window.queue.RestoreActivity(window.activity)
		}
		m.catalogReporter.Printf("project=%q instance_id=%q: 发布日志名称目录失败: %v", m.id.Project, m.id.InstanceID, err)
	}
}
