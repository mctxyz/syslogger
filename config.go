package syslogger

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	defaultFlushInterval      = 10 * time.Second
	defaultMaxLabelValueBytes = 2048
	// 保持既有身份标签容量的合法配置范围。
	minimumMaxLabelValueBytes = 21
	// 队满后的默认观察期以连续无丢弃时间判断恢复。
	defaultQueueFullRecoveryInterval = 30 * time.Second
	// defaultQueueCapacity 是未配置时使用的模块日志队列容量。
	defaultQueueCapacity = 512
	// maximumQueueCapacity 限定单模块指针队列的容量范围。
	maximumQueueCapacity = 4096
)

// Config 固定一个项目运行实例的身份、状态维护及指标出口配置。
type Config struct {
	// Project 由接入项目的业务代码固定提供。
	Project string
	// InstanceID 由业务入口提供，空字符串使用 common，跨重启复用。
	InstanceID string
	// SyslogSocketPath 指定本机 Unix socket；空值按系统标准路径发现。
	SyslogSocketPath string
	// DefaultQueueCapacity 指定模块日志队列的默认容量，零值为 512，合法范围为 1～4096。
	DefaultQueueCapacity int
	// StateEnabled 开启同步状态维护，零值关闭。
	StateEnabled bool
	// DegradeOnQueueFull 按模块队满丢弃自动降级，零值关闭，启用时需要 StateEnabled。
	DegradeOnQueueFull bool
	// QueueFullRecoveryInterval 是连续无队满丢弃的恢复等待时间，启用自动降级时零值为 30 秒。
	QueueFullRecoveryInterval time.Duration
	// HTTP 配置已有服务挂载或专用监听的指标能力。
	HTTP HTTPConfig
	// File 配置周期文件交付，零值关闭。
	File FileConfig
	// MaxLabelValueBytes 限制转义前身份标签的字节数，零值为 2048。
	MaxLabelValueBytes int
}

// HTTPConfig 定义固定生命周期的 HTTP 指标能力。
type HTTPConfig struct {
	Enabled bool
	// ListenAddr 供 StartHTTP 使用；外部挂载使用业务服务器的地址。
	ListenAddr string
}

// FileConfig 定义指标文件的完整目标路径及刷新周期。
type FileConfig struct {
	Enabled bool
	// OutputPath 包含目录和文件名，父目录由接入方准备；相对路径基于当前工作目录。
	OutputPath string
	// FlushInterval 零值为 10 秒，首次快照由 StartFile 同步写入。
	FlushInterval time.Duration
}

func (cfg Config) normalized() (Config, error) {
	cfg = cfg.withDefaults()
	if cfg.MaxLabelValueBytes < minimumMaxLabelValueBytes {
		return Config{}, fmt.Errorf("syslogger: 标签上限至少为 %d 字节", minimumMaxLabelValueBytes)
	}
	if err := validateName(cfg.Project, cfg.MaxLabelValueBytes); err != nil {
		return Config{}, fmt.Errorf("syslogger: Project: %w", err)
	}
	if err := validateInstanceID(cfg.InstanceID, cfg.MaxLabelValueBytes); err != nil {
		return Config{}, err
	}
	if cfg.DefaultQueueCapacity < 1 || cfg.DefaultQueueCapacity > maximumQueueCapacity {
		return Config{}, fmt.Errorf("syslogger: DefaultQueueCapacity 必须为 1～%d", maximumQueueCapacity)
	}
	if !cfg.StateEnabled && (cfg.HTTP.Enabled || cfg.File.Enabled) {
		return Config{}, fmt.Errorf("syslogger: HTTP 或文件出口启用时必须开启 StateEnabled")
	}
	if cfg.DegradeOnQueueFull {
		if !cfg.StateEnabled {
			return Config{}, fmt.Errorf("syslogger: 队满自动降级启用时必须开启 StateEnabled")
		}
		if cfg.QueueFullRecoveryInterval < 0 {
			return Config{}, fmt.Errorf("syslogger: QueueFullRecoveryInterval 必须为非负值")
		}
	}
	if cfg.File.Enabled {
		if strings.TrimSpace(cfg.File.OutputPath) == "" {
			return Config{}, fmt.Errorf("syslogger: 文件出口需要 OutputPath")
		}
		if cfg.File.FlushInterval < 0 {
			return Config{}, fmt.Errorf("syslogger: FlushInterval 必须为非负值")
		}
	}
	return cfg, nil
}

func (cfg Config) withDefaults() Config {
	if cfg.InstanceID == "" {
		cfg.InstanceID = "common"
	}
	if cfg.MaxLabelValueBytes == 0 {
		cfg.MaxLabelValueBytes = defaultMaxLabelValueBytes
	}
	if cfg.DefaultQueueCapacity == 0 {
		cfg.DefaultQueueCapacity = defaultQueueCapacity
	}
	if cfg.File.Enabled && cfg.File.FlushInterval == 0 {
		cfg.File.FlushInterval = defaultFlushInterval
	}
	if cfg.DegradeOnQueueFull && cfg.QueueFullRecoveryInterval == 0 {
		cfg.QueueFullRecoveryInterval = defaultQueueFullRecoveryInterval
	}
	return cfg
}

func validateInstanceID(id string, maxBytes int) error {
	validLength := len(id) >= 3 && len(id) <= 128
	withinLabelLimit := len(id) <= maxBytes
	if !validLength || !withinLabelLimit || !utf8.ValidString(id) {
		return fmt.Errorf("syslogger: InstanceID 必须为 3～128 字节且符合标签上限的合法 UTF-8")
	}
	return nil
}

func validateName(name string, maxBytes int) error {
	if name == "" || strings.TrimSpace(name) == "" {
		return fmt.Errorf("名称必须包含非空白字符")
	}
	if len(name) > maxBytes || !utf8.ValidString(name) {
		return fmt.Errorf("名称必须为符合标签上限的合法 UTF-8")
	}
	return nil
}
