// Package journalwire 定义 SDK、init 与 CLI 共用的四组 journal 入口、身份字段和消息编码。
package journalwire

import (
	"bytes"
	"encoding/binary"
	"os"
	"slices"
	"strconv"

	"github.com/mctxyz/syslogger/internal/identity"
)

const (
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

// AppendMessage 向调用方独占的缓冲追加原生消息，保留原 JSON 字节及既有前缀。
// dst 的可写空间应与 header、body 独立，发送完成后调用方可复用返回的缓冲。
func AppendMessage(dst, header, body []byte, severity uint8) []byte {
	data := slices.Grow(dst, len(header)+len(body)+64)
	data = append(data, header...)
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
