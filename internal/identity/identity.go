// Package identity 定义日志和指标共用的运行实例身份。
package identity

// Instance 的字段在管理器生命周期内固定，原文参与日志和指标输出。
type Instance struct {
	Project    string `json:"project"`
	InstanceID string `json:"instance_id"`
}
