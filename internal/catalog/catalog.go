// Package catalog 保存本机日志查询使用的项目、实例和组件目录。
package catalog

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
)

// Root 是 SDK 和日志工具共同使用的固定目录，与进程 TMPDIR 无关。
const Root = "/tmp/mct-syslogger"

// Component 保存组件的日志身份及供用户选择时阅读的描述。
type Component struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Instance 是某个实例最近一次发布的完整日志名称目录。
type Instance struct {
	Project             string      `json:"project"`
	InstanceID          string      `json:"instance_id"`
	InstanceDescription string      `json:"instance_description"`
	Components          []Component `json:"components"`
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
