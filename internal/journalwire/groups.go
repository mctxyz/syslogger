package journalwire

// Group 是独立管理容量和轮转的固定日志等级组。
type Group uint8

const (
	// DebugGroup 至 ErrorGroup 按四项容量参数的展示顺序排列。
	DebugGroup Group = iota
	InfoGroup
	WarningGroup
	ErrorGroup
	// GroupCount 限定四个固定空间及其连接槽位。
	GroupCount
)

// GroupForSeverity 将原生八级日志映射到互斥空间；notice 与 info 共用留存预算。
func GroupForSeverity(severity uint8) Group {
	switch severity {
	case 7:
		return DebugGroup
	case 6, 5:
		return InfoGroup
	case 4:
		return WarningGroup
	default:
		return ErrorGroup
	}
}

// Name 是容量参数和命名空间共同使用的固定分组名称。
func (g Group) Name() string {
	return [...]string{"debug", "info", "warning", "error"}[g]
}

// Namespace 是等级组对应的专用 journald 实例标识。
func (g Group) Namespace() string {
	return "mct-syslogger-" + g.Name()
}

// SocketPath 是对应实例的原生数据报入口，由 init 准备。
func (g Group) SocketPath() string {
	return "/run/systemd/journal." + g.Namespace() + "/socket"
}

// MaxSeverity 是该组保存完整等级集合所需的最大原生 severity。
func (g Group) MaxSeverity() uint8 {
	return [...]uint8{7, 6, 4, 3}[g]
}
