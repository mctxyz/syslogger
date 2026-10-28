package syslogger

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/mctxyz/syslogger/internal/catalog"
	"github.com/mctxyz/syslogger/internal/diagnostic"
	"github.com/mctxyz/syslogger/internal/identity"
	"github.com/mctxyz/syslogger/internal/logging"
	"github.com/prometheus/client_golang/prometheus"
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
	metricsReporter      diagnostic.Reporter
	catalogReporter      diagnostic.Reporter
	catalog              *catalog.Store
	catalogMu            sync.Mutex
	registry             *prometheus.Registry
	metricDescs          [len(metricDefinitions)]*prometheus.Desc
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

// New 校验配置并注册内置 Collector，启用日志名称目录时发布初始清单。
// 日志交付及指标出口的文件和 socket I/O 由各自的实际出口执行。
func New(cfg Config) (*Manager, error) {
	cfg, err := cfg.normalized()
	if err != nil {
		return nil, err
	}
	m := newManager(cfg)
	if err := m.registry.Register(m); err != nil {
		return nil, fmt.Errorf("syslogger: 注册内置指标: %w", err)
	}
	m.publishCatalog()
	return m, nil
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
	if cfg.CatalogEnabled {
		m.catalog = &catalog.Store{Root: catalog.Root}
	}
	m.registry = cfg.Registry
	if m.registry == nil {
		m.registry = prometheus.NewRegistry()
	}
	labels := prometheus.Labels{
		"project":     m.id.Project,
		"instance_id": m.id.InstanceID,
	}
	for index, definition := range metricDefinitions {
		m.metricDescs[index] = prometheus.NewDesc(
			definition.name,
			definition.help,
			[]string{"component"},
			labels,
		)
	}
	return m
}

// ComponentConfig 定义模块的日志身份、展示描述和独立队列容量。
type ComponentConfig struct {
	Name        string
	Description string
	// QueueCapacity 为 1～4096 时使用原值，其余值采用 Config.DefaultQueueCapacity。
	QueueCapacity int
}

// Register 根据配置建立独立模块，成功后更新已启用的日志名称目录。
// 模块集合在交出 Handler、开始启动指标出口或首次 Registry 采集时固定。
func (m *Manager) Register(cfg ComponentConfig) (component *Component, err error) {
	name, queueCapacity := cfg.Name, cfg.QueueCapacity
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
		} else {
			m.publishCatalog()
		}
	}()
	if m.closing || m.frozen {
		return nil, fmt.Errorf("syslogger: 当前生命周期阶段无法注册组件")
	}
	kind := ""
	if nameErr := validateName(name); nameErr != nil {
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
	if queueCapacity < 1 || queueCapacity > maximumQueueCapacity {
		queueCapacity = m.cfg.DefaultQueueCapacity
	}
	component = &Component{manager: m, name: name, description: cfg.Description}
	m.components[name] = component
	added = true
	if m.cfg.StateEnabled {
		m.diagnostic.Registered(time.Now())
	}
	component.logs = logging.New(queueCapacity, m.cfg.SyslogSocketPath, m.id.Project, m.id.InstanceID, name)
	return component, nil
}
