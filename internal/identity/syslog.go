package identity

import (
	"strings"
	"unicode"
)

// SyslogTag 将项目原文转换为模块 syslog 协议头的标识，供发送与 journal 预筛共享。
// 协议头的空白及控制字符转换为下划线，JSON 正文中的项目原文保持不变。
func SyslogTag(project string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return '_'
		}
		return r
	}, project)
}
