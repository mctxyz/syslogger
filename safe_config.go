package syslogger

import (
	"fmt"
	"strings"

	"github.com/mctxyz/syslogger/internal/logging"
)

// configCorrection 保存已纠正字段及可公开的诊断信息，原始配置由接入方保管。
type configCorrection struct {
	field     string
	reason    string
	effective any
	level     logging.Level
}

func (cfg Config) corrected() (Config, []configCorrection) {
	var corrections []configCorrection
	// 先确定有效的指标文件出口，再应用默认值并补齐状态维护依赖。
	if cfg.File.Enabled && strings.TrimSpace(cfg.File.OutputPath) == "" {
		cfg.File.Enabled = false
		corrections = append(corrections, configCorrection{
			field:     "File.Enabled",
			reason:    "File.OutputPath 为空，已停用指标文件出口",
			effective: false,
			level:     logging.Error,
		})
	}
	cfg = cfg.withDefaults()
	if cfg.MaxLabelValueBytes < minimumMaxLabelValueBytes {
		cfg.MaxLabelValueBytes = defaultMaxLabelValueBytes
		corrections = append(corrections, configCorrection{
			field:     "MaxLabelValueBytes",
			reason:    fmt.Sprintf("标签上限必须至少为 %d 字节", minimumMaxLabelValueBytes),
			effective: cfg.MaxLabelValueBytes,
			level:     logging.Warning,
		})
	}
	if cfg.Project == "" {
		cfg.Project = "common"
		corrections = append(corrections, configCorrection{
			field:     "Project",
			reason:    "项目名为空",
			effective: cfg.Project,
			level:     logging.Warning,
		})
	} else if err := validateName(cfg.Project, cfg.MaxLabelValueBytes); err != nil {
		cfg.Project = "unknown"
		corrections = append(corrections, configCorrection{
			field:     "Project",
			reason:    err.Error(),
			effective: cfg.Project,
			level:     logging.Warning,
		})
	}
	if err := validateInstanceID(cfg.InstanceID, cfg.MaxLabelValueBytes); err != nil {
		cfg.InstanceID = "common"
		corrections = append(corrections, configCorrection{
			field:     "InstanceID",
			reason:    err.Error(),
			effective: cfg.InstanceID,
			level:     logging.Warning,
		})
	}
	if cfg.File.Enabled && cfg.File.FlushInterval < 0 {
		cfg.File.FlushInterval = defaultFlushInterval
		corrections = append(corrections, configCorrection{
			field:     "File.FlushInterval",
			reason:    "指标文件刷新间隔为负数",
			effective: cfg.File.FlushInterval.String(),
			level:     logging.Warning,
		})
	}
	if !cfg.StateEnabled && (cfg.HTTP.Enabled || cfg.File.Enabled) {
		cfg.StateEnabled = true
		corrections = append(corrections, configCorrection{
			field:     "StateEnabled",
			reason:    "已启用的指标出口需要状态维护",
			effective: true,
			level:     logging.Warning,
		})
	}
	return cfg, corrections
}
