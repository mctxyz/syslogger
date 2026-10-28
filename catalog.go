package syslogger

import (
	"sort"

	"github.com/mctxyz/syslogger/internal/catalog"
	"github.com/mctxyz/syslogger/internal/diagnostic"
)

// publishCatalog 串行读取最新组件集合后发布，文件 I/O 在集合锁外执行。
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
	if m.diagnostic != nil || m.startupLogs != nil {
		instance.Components = append(instance.Components, catalog.Component{
			Name:        diagnostic.ComponentName,
			Description: "管理器初始化与组件注册诊断",
		})
	}
	for _, component := range m.components {
		instance.Components = append(instance.Components, catalog.Component{
			Name:        component.name,
			Description: component.description,
		})
	}
	m.mu.RUnlock()
	sort.Slice(instance.Components, func(i, j int) bool {
		return instance.Components[i].Name < instance.Components[j].Name
	})
	if err := m.catalog.Write(instance); err != nil {
		m.catalogReporter.Printf("project=%q instance_id=%q: 发布日志名称目录失败: %v", m.id.Project, m.id.InstanceID, err)
	}
}
