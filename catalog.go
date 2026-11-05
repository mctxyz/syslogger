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
	m.publishCatalog()
	ticker := time.NewTicker(catalog.SyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.catalogWake:
			m.publishCatalog()
		case <-ticker.C:
			m.publishCatalog()
			err := m.catalog.Prune(time.Now().UTC())
			m.catalogResult(diagnostic.CatalogCleanup, err)
			if err != nil {
				m.catalogReporter.Printf("project=%q instance_id=%q: 清理过期日志名称目录失败: %v", m.id.Project, m.id.InstanceID, err)
			}
		case <-m.catalogStop:
			return
		}
	}
}

// publishCatalog 在集合锁外发布活动，失败后恢复窗口；空闲周期复查根目录以恢复可用性。
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
	err := m.catalog.CheckRoot()
	if err == nil && len(windows) > 0 {
		err = m.catalog.Merge(instance, time.Now().UTC())
	}
	m.catalogResult(diagnostic.CatalogPublication, err)
	if err != nil {
		for _, window := range windows {
			window.queue.RestoreActivity(window.activity)
		}
		m.catalogReporter.Printf("project=%q instance_id=%q: 发布日志名称目录失败: %v", m.id.Project, m.id.InstanceID, err)
	}
}

// catalogResult 在集合锁内合成管理器健康，不改变业务组件和关闭结果。
func (m *Manager) catalogResult(operation diagnostic.CatalogOperation, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.diagnostic != nil {
		m.diagnostic.CatalogResult(operation, err != nil, time.Now())
	}
}
