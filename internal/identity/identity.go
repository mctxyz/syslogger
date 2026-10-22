// Package identity 定义日志、指标和文件命名共用的运行实例身份。
package identity

// Instance 的字段在管理器生命周期内固定，原文参与日志和指标输出。
type Instance struct {
	Project    string `json:"project"`
	InstanceID string `json:"instance_id"`
}
