package main

import (
	"bytes"
	"time"
	"unicode"

	json "github.com/goccy/go-json"
)

// logRecord 一次提取基础字段，保留本条输入，供 fields 延迟展开和 JSON 原文输出使用。
// Fields 仅校验对象或 null；空结构体让解码器验证内容而不构建动态对象树。
type logRecord struct {
	Timestamp  string   `json:"timestamp"`
	Level      string   `json:"level"`
	Project    string   `json:"project"`
	InstanceID string   `json:"instance_id"`
	Component  string   `json:"component"`
	Message    *string  `json:"message"`
	Fields     struct{} `json:"fields"`
	line       []byte
	fieldsText *string
	stats      *queryStats
}

func parseRecord(line []byte) (logRecord, bool) {
	var record logRecord
	// 前导仅允许 JSON 空白；原实现允许根值后的 Unicode 空白，故只在右侧统一清理。
	line = bytes.TrimRightFunc(line, unicode.IsSpace)
	start := bytes.TrimLeft(line, " \t\r\n")
	if len(start) == 0 || start[0] != '{' {
		return record, false
	}
	if err := json.Unmarshal(line, &record); err != nil {
		return record, false
	}
	if record.Timestamp == "" || record.Project == "" || record.InstanceID == "" ||
		record.Component == "" || record.Message == nil || !contains(levels, record.Level) {
		return record, false
	}
	// filterLogs 的 ReadBytes 为每行交付独立切片；记录仅在本轮处理期间持有该输入。
	record.line = line
	return record, true
}

// fieldsJSON 按需展开一次，并缓存规范化文本供内容筛选和显示复用；空字段也参与缓存。
func (record *logRecord) fieldsJSON() (string, error) {
	if record.fieldsText != nil {
		return *record.fieldsText, nil
	}
	var content struct {
		Fields map[string]any `json:"fields"`
	}
	decoder := json.NewDecoder(bytes.NewReader(record.line))
	decoder.UseNumber()
	var phase time.Time
	if record.stats != nil {
		record.stats.fieldsDecodes++
		phase = time.Now()
	}
	decodeErr := decoder.Decode(&content)
	if record.stats != nil {
		record.stats.fieldsDecode += time.Since(phase)
	}
	if decodeErr != nil {
		return "", decodeErr
	}
	text := ""
	if len(content.Fields) > 0 {
		if record.stats != nil {
			record.stats.fieldsEncodes++
			phase = time.Now()
		}
		encoded, err := json.MarshalWithOption(content.Fields, json.DisableHTMLEscape())
		if record.stats != nil {
			record.stats.fieldsEncode += time.Since(phase)
		}
		if err != nil {
			return "", err
		}
		text = string(encoded)
	}
	record.fieldsText = &text
	return text, nil
}
