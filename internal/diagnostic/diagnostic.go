// Package diagnostic 维护管理器自身事实及独立的运行诊断输出。
package diagnostic

import (
	"log"
	"sync"
	"time"

	"github.com/mctxyz/syslogger/internal/record"
)

const (
	// ComponentName 是管理器的保留组件名称。
	ComponentName = "syslogger_manager"
	// RegisterNameInvalid 表达名称校验失败。
	RegisterNameInvalid = "register_name_invalid"
	// RegisterDuplicate 表达精确同名冲突。
	RegisterDuplicate = "register_duplicate"
	// RegisterReserved 表达保留名称冲突。
	RegisterReserved = "register_reserved"
)

// Tracker 由管理器的集合锁保护，分类只用于内部恢复条件。
type Tracker struct {
	Current record.Record
	kind    string
}

// New 在成功创建时建立初始化健康事实。
func New(at time.Time) *Tracker { return &Tracker{Current: record.New(1, at)} }

// RegistrationFailed 记录已分类的注册受阻事实。
func (t *Tracker) RegistrationFailed(kind string, at time.Time) {
	if !at.Before(t.Current.ObservedAt) {
		t.Current = record.New(2, at)
		t.kind = kind
	}
}

// Registered 在实际成功注册时恢复当前注册受阻问题。
func (t *Tracker) Registered(at time.Time) {
	if t.kind != "" && !at.Before(t.Current.ObservedAt) {
		t.Current = record.New(1, at)
		t.kind = ""
	}
}

// Reporter 对同一资源的连续运行错误限流，输出独立于业务日志队列。
type Reporter struct {
	mu   sync.Mutex
	last time.Time
}

// Printf 最多每秒输出一次摘要，累计次数由对应指标完整保留。
func (r *Reporter) Printf(format string, args ...any) {
	now := time.Now()
	r.mu.Lock()
	if !r.last.IsZero() && now.Sub(r.last) < time.Second {
		r.mu.Unlock()
		return
	}
	r.last = now
	r.mu.Unlock()
	log.Printf("syslogger: "+format, args...)
}
