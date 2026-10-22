// Package logging 负责事件编码、模块指针队列及本机 syslog 传输。
package logging

import (
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/mctxyz/syslogger/internal/identity"
)

// Level 按 syslog 原生 severity 从严重到调试排列。
type Level uint8

const (
	// Emergency 至 Debug 与原生 severity 0～7 一一对应。
	Emergency Level = iota
	Alert
	Critical
	Error
	Warning
	Notice
	Info
	Debug
)

var levelNames = [...]string{"emergency", "alert", "critical", "error", "warning", "notice", "info", "debug"}

// Entry 以单指针入队，正文由本次编码独立持有。
type Entry struct {
	Severity Level
	Body     []byte
}

// Encode 在调用线程冻结结构化字段；调用期间字段由业务保证无并发修改。
func Encode(id identity.Instance, component string, level Level, message string, fields map[string]any, at time.Time) (*Entry, error) {
	if level > Debug {
		return nil, fmt.Errorf("syslogger: 未知日志等级 %d", level)
	}
	var values map[string]any
	if len(fields) != 0 {
		values = make(map[string]any, len(fields))
		for key, value := range fields {
			if err, ok := value.(error); ok {
				v := reflect.ValueOf(err)
				if (v.Kind() == reflect.Ptr || v.Kind() == reflect.Map || v.Kind() == reflect.Slice || v.Kind() == reflect.Interface || v.Kind() == reflect.Func || v.Kind() == reflect.Chan) && v.IsNil() {
					value = nil
				} else {
					value = err.Error()
				}
			}
			values[key] = value
		}
	}
	data, err := json.Marshal(struct {
		Timestamp time.Time `json:"timestamp"`
		Level     string    `json:"level"`
		identity.Instance
		Component string         `json:"component"`
		Message   string         `json:"message"`
		Fields    map[string]any `json:"fields,omitempty"`
	}{at.UTC(), levelNames[level], id, component, message, values})
	if err != nil {
		return nil, fmt.Errorf("syslogger: 编码日志: %w", err)
	}
	return &Entry{Severity: level, Body: data}, nil
}
