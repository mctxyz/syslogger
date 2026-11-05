// Package journalwire 定义 SDK 与 CLI 共用的 journal 命名空间、身份字段和固定消息编码。
package journalwire

import (
	"bytes"
	"encoding/binary"
	"os"
	"strconv"

	"github.com/mctxyz/syslogger/internal/identity"
)

const (
	// Namespace 是本模块日志流使用的固定 journal 命名空间。
	Namespace = "mct-syslogger"
	// SocketPath 是专用 journald 实例的原生数据报入口，由部署方启用。
	SocketPath = "/run/systemd/journal." + Namespace + "/socket"
	// ProjectField 保存项目原文，用于独立于规范化系统日志 tag 的身份索引。
	ProjectField = "SYSLOGGER_PROJECT"
	// InstanceField 和 ComponentField 将稳定身份交给 journal 原生索引。
	InstanceField  = "SYSLOGGER_INSTANCE_ID"
	ComponentField = "SYSLOGGER_COMPONENT"
)

// Header 编码组件固定字段，连接建立后可复用，日志正文和等级在每条消息中追加。
func Header(id identity.Instance, component string) []byte {
	data := make([]byte, 0, 256)
	for _, field := range []struct {
		name  string
		value string
	}{
		{ProjectField, id.Project},
		{InstanceField, id.InstanceID},
		{ComponentField, component},
		{"SYSLOG_IDENTIFIER", identity.SyslogTag(id.Project)},
		{"SYSLOG_FACILITY", "1"},
		{"SYSLOG_PID", strconv.Itoa(os.Getpid())},
	} {
		data = appendField(data, field.name, []byte(field.value))
	}
	return data
}

// Message 保留原 JSON 字节，并使用独立缓冲区，保证固定头可在后续消息中继续复用。
func Message(header, body []byte, severity uint8) []byte {
	data := make([]byte, len(header), len(header)+len(body)+64)
	copy(data, header)
	data = appendField(data, "PRIORITY", []byte(strconv.Itoa(int(severity))))
	return appendField(data, "MESSAGE", body)
}

func appendField(data []byte, name string, value []byte) []byte {
	data = append(data, name...)
	if bytes.IndexByte(value, '\n') >= 0 {
		// 原生协议以长度前缀编码含换行的值，身份原文无需规范化或丢弃字符。
		data = append(data, '\n')
		data = binary.LittleEndian.AppendUint64(data, uint64(len(value)))
	} else {
		data = append(data, '=')
	}
	data = append(data, value...)
	return append(data, '\n')
}
