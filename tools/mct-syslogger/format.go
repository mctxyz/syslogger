package main

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// 彩色片段指定前景色或弱化亮度，每个片段结束后恢复终端样式。
const (
	colourReset     = "\x1b[0m"
	colourProject   = "\x1b[35m"
	colourInstance  = "\x1b[34m"
	colourComponent = "\x1b[36m"
	colourMessage   = "\x1b[32m"
	// 业务字段沿用终端默认前景色并弱化亮度，与正常亮度的时间及其他彩色片段区分。
	colourFields = "\x1b[2m"
)

// levelColours 按严重程度分配独立样式，紧急事件通过背景及加粗突出显示。
var levelColours = map[string]string{
	"emergency": "\x1b[1;97;41m",
	"alert":     "\x1b[1;95m",
	"critical":  "\x1b[91m",
	"error":     "\x1b[31m",
	"warning":   "\x1b[33m",
	"notice":    "\x1b[36m",
	"info":      "\x1b[32m",
	"debug":     "\x1b[90m",
}

func escaped(value string) string {
	quoted := strconv.Quote(value)
	return quoted[1 : len(quoted)-1]
}

func colourText(text string, style string) string {
	return style + text + colourReset
}

func (record *logRecord) format(asJSON bool, colour bool) (string, error) {
	if asJSON {
		return string(bytes.TrimSpace(record.line)), nil
	}
	// 可读时间按运行设备的系统时区显示到秒，无法解析时保留转义后的原文供排查。
	timestamp := escaped(record.Timestamp)
	if at, err := time.Parse(time.RFC3339Nano, record.Timestamp); err == nil {
		timestamp = at.Local().Format("2006-01-02 15:04:05")
	}
	// 等级先按可见字符补齐宽度，再添加 ANSI 样式，保持各行列对齐。
	level := fmt.Sprintf("%-9s", strings.ToUpper(escaped(record.Level)))
	project := escaped(record.Project)
	instance := escaped(record.InstanceID)
	component := escaped(record.Component)
	message := escaped(*record.Message)
	fields, err := record.fieldsJSON()
	if err != nil {
		return "", err
	}
	if colour {
		level = colourText(level, levelColours[record.Level])
		project = colourText(project, colourProject)
		instance = colourText(instance, colourInstance)
		component = colourText(component, colourComponent)
		message = colourText(message, colourMessage)
		if fields != "" {
			fields = colourText(fields, colourFields)
		}
	}
	text := fmt.Sprintf(
		"%s %s %s/%s/%s %s",
		timestamp,
		level,
		project,
		instance,
		component,
		message,
	)
	if fields != "" {
		text += " " + fields
	}
	return text, nil
}
