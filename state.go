// Package syslogger 提供本机 syslog 异步交付和业务主动汇报的组件健康指标。
package syslogger

// State 是组件当前健康结论，同时作为状态 Gauge 的数值。
type State int

const (
	// StateUnknown 表示业务组件尚未汇报。
	StateUnknown State = iota
	// StateHealthy 表示能够正常履行职责。
	StateHealthy
	// StateDegraded 表示部分能力受损。
	StateDegraded
	// StateUnhealthy 表示无法正常履行职责。
	StateUnhealthy
)
