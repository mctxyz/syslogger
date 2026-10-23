package syslogger

import (
	"log"
	"time"

	"github.com/mctxyz/syslogger/internal/diagnostic"
	"github.com/mctxyz/syslogger/internal/logging"
)

// SafeNew 纠正非法配置并返回完整可用的管理器，后续方法沿用各自的错误契约。
// 初始化异常通过标准日志及可用的 syslog 报告，启用状态维护时持续标记管理器不健康。
func SafeNew(cfg Config) *Manager {
	cfg, corrections := cfg.corrected()
	m := newManager(cfg)
	if len(corrections) == 0 {
		return m
	}
	if m.diagnostic != nil {
		m.diagnostic.ConfigurationFailed(time.Now())
	}
	for _, correction := range corrections {
		level := "warning"
		if correction.level == logging.Error {
			level = "error"
		}
		log.Printf(
			"syslogger: project=%q instance_id=%q component=%q state=unhealthy level=%s field=%q reason=%q effective=%v",
			m.id.Project,
			m.id.InstanceID,
			diagnostic.ComponentName,
			level,
			correction.field,
			correction.reason,
			correction.effective,
		)
	}
	// 标准日志已保留全部纠正，syslog 补充交付使用独立队列并由管理器关闭。
	m.startupLogs = logging.New(
		len(corrections),
		cfg.SyslogSocketPath,
		m.id.Project,
		m.id.InstanceID,
		diagnostic.ComponentName,
	)
	for _, correction := range corrections {
		fields := map[string]any{
			"state":     "unhealthy",
			"field":     correction.field,
			"reason":    correction.reason,
			"effective": correction.effective,
		}
		entry, err := logging.Encode(
			m.id,
			diagnostic.ComponentName,
			correction.level,
			"初始化配置已纠正，管理器状态为 Unhealthy",
			fields,
			time.Now(),
		)
		if err != nil {
			log.Printf("syslogger: 初始化诊断编码失败: %v", err)
			continue
		}
		if err := m.startupLogs.Enqueue(entry); err != nil {
			log.Printf("syslogger: 初始化诊断入队失败: %v", err)
		}
	}
	return m
}
