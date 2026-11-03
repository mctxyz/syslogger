// Package catalog 保存本机日志查询使用的项目、实例和组件目录。
package catalog

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"time"
)

// Root 是 SDK 和日志工具共同使用的固定目录，与进程 TMPDIR 无关。
const Root = "/var/lib/mct-syslogger"

// SyncInterval 是 SDK 批量同步周期，也是物理清理为未同步活动保留的宽限窗口。
const SyncInterval = 10 * time.Minute

// Component 保存组件日志身份、描述及当前记录生命周期内的成功接收统计。
type Component struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	// LastActiveAt 来自实际成功接收时刻，后台检查本身不更新它。
	LastActiveAt time.Time `json:"last_active_at"`
	// LogCount 保存累计接收量，后续发送结果不回减，溢出自然回绕。
	LogCount uint64 `json:"log_count"`
}

// Instance 保存实例的历史日志名称；每个组件维护自己的活动时间与累计接收量。
type Instance struct {
	Project             string      `json:"project"`
	InstanceID          string      `json:"instance_id"`
	InstanceDescription string      `json:"instance_description"`
	CreatedAt           time.Time   `json:"created_at"`
	Components          []Component `json:"components"`
}

// Active 按最后一次成功接收时间保留一年内的组件，展示使用当前时间，物理清理额外保留同步窗口。
func (instance Instance) Active(now time.Time) Instance {
	cutoff := now.AddDate(-1, 0, 0)
	components := make([]Component, 0, len(instance.Components))
	for _, component := range instance.Components {
		if !component.LastActiveAt.IsZero() && !component.LastActiveAt.Before(cutoff) {
			components = append(components, component)
		}
	}
	instance.Components = components
	return instance
}

// Store 负责给定根目录中的目录文件，发布与读取共用相同路径约定。
type Store struct {
	Root string
}

// Path 将原文身份映射为固定长度路径，业务名称保存在 JSON 中。
func (s Store) Path(project, instance string) string {
	projectKey := fmt.Sprintf("%x", sha256.Sum256([]byte(project)))
	instanceKey := fmt.Sprintf("%x.json", sha256.Sum256([]byte(instance)))
	return filepath.Join(s.Root, projectKey, instanceKey)
}
