package syslogger

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/mctxyz/syslogger/internal/diagnostic"
	"github.com/mctxyz/syslogger/internal/identity"
	"github.com/mctxyz/syslogger/internal/logging"
	"github.com/mctxyz/syslogger/internal/metrics"
)

type outletPhase uint8

const (
	outletIdle outletPhase = iota
	outletStarting
	outletRunning
)

// Manager 持有固定实例配置、组件集合及库拥有的出口和关闭流程，由 New 或 SafeNew 创建。
type Manager struct {
	mu                   sync.RWMutex
	cfg                  Config
	id                   identity.Instance
	components           map[string]*Component
	diagnostic           *diagnostic.Tracker
	startupLogs          *logging.Queue
	registrationReporter diagnostic.Reporter
	httpReporter         diagnostic.Reporter
	frozen               bool
	closing              bool
	starters             sync.WaitGroup
	httpPhase            outletPhase
	server               *http.Server
	httpDone             chan struct{}
	filePhase            outletPhase
	fileStop             chan struct{}
	fileDone             chan struct{}
	done                 chan struct{}
	finalErr             error
}

// New 归一化实例默认值、校验配置并准备内存；文件和 socket I/O 由实际出口执行。
func New(cfg Config) (*Manager, error) {
	cfg, err := cfg.normalized()
	if err != nil {
		return nil, err
	}
	return newManager(cfg), nil
}

// newManager 为两个构造入口建立相同的完整内部状态，配置由各入口完成处理。
func newManager(cfg Config) *Manager {
	m := &Manager{
		cfg: cfg,
		id: identity.Instance{
			Project:    cfg.Project,
			InstanceID: cfg.InstanceID,
		},
		components: make(map[string]*Component),
		done:       make(chan struct{}),
	}
	if cfg.StateEnabled {
		m.diagnostic = diagnostic.New(time.Now())
	}
	return m
}

// Register 建立独立模块；容量 1～4096 使用原值，其余值回退 512。
// 模块集合在交出 Handler 或开始启动指标出口时固定。
func (m *Manager) Register(name string, queueCapacity int) (component *Component, err error) {
	m.mu.Lock()
	added := false
	defer func() {
		if value := recover(); value != nil {
			if added {
				delete(m.components, name)
			}
			component = nil
			err = fmt.Errorf("syslogger: 注册组件时发生 panic: %v", value)
		}
		m.mu.Unlock()
		if err != nil {
			m.registrationReporter.Printf("project=%q instance_id=%q: 注册组件失败: %v", m.id.Project, m.id.InstanceID, err)
		}
	}()
	if m.closing || m.frozen {
		return nil, fmt.Errorf("syslogger: 当前生命周期阶段无法注册组件")
	}
	kind := ""
	if nameErr := validateName(name, m.cfg.MaxLabelValueBytes); nameErr != nil {
		kind = diagnostic.RegisterNameInvalid
		err = fmt.Errorf("syslogger: 注册名称: %w", nameErr)
	} else if name == diagnostic.ComponentName {
		kind = diagnostic.RegisterReserved
		err = fmt.Errorf("syslogger: 组件名称 %q 为管理器保留名称", name)
	} else if _, exists := m.components[name]; exists {
		kind = diagnostic.RegisterDuplicate
		err = fmt.Errorf("syslogger: 组件名称 %q 已注册", name)
	}
	if err != nil {
		if m.cfg.StateEnabled {
			m.diagnostic.RegistrationFailed(kind, time.Now())
		}
		return nil, err
	}
	component = &Component{manager: m, name: name}
	m.components[name] = component
	added = true
	if m.cfg.StateEnabled {
		m.diagnostic.Registered(time.Now())
	}
	component.logs = logging.New(queueCapacity, m.cfg.SyslogSocketPath, m.id.Project, m.id.InstanceID, name)
	return component, nil
}

func (m *Manager) snapshot() []metrics.Component {
	m.mu.RLock()
	components := make([]*Component, 0, len(m.components))
	for _, component := range m.components {
		components = append(components, component)
	}
	var result []metrics.Component
	if m.diagnostic != nil {
		result = append(result, metrics.Component{Name: diagnostic.ComponentName, Record: m.diagnostic.Current})
	}
	m.mu.RUnlock()
	for _, component := range components {
		component.mu.RLock()
		item := metrics.Component{Name: component.name, Record: component.current, HasLogs: true}
		component.mu.RUnlock()
		item.Rejected, item.SendFailures = component.logs.Counts()
		result = append(result, item)
	}
	return result
}
