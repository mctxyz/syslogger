// Package record 维护业务及管理器共用的完整健康记录及时间选择。
package record

import "time"

// Record 保存同次汇报的状态和时间，Reported 区分初始记录。
type Record struct {
	State      int
	ObservedAt time.Time
	Reported   bool
}

// New 用入口时间准备一条完整汇报。
func New(state int, at time.Time) Record {
	return Record{State: state, ObservedAt: at, Reported: true}
}

// Apply 选择较新的完整记录；时间相同由后提交者覆盖。
func (r *Record) Apply(next Record) {
	if !r.Reported || !next.ObservedAt.Before(r.ObservedAt) {
		*r = next
	}
}
