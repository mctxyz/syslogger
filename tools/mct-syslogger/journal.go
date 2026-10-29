package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// levels 与库输出的八级日志正文名称一致。
var levels = []string{
	"emergency",
	"alert",
	"critical",
	"error",
	"warning",
	"notice",
	"info",
	"debug",
}

// 字段使用固定 ANSI 前景色，每个片段结束后恢复终端样式。
const (
	colourReset     = "\x1b[0m"
	colourProject   = "\x1b[35m"
	colourInstance  = "\x1b[34m"
	colourComponent = "\x1b[36m"
	colourMessage   = "\x1b[32m"
	colourFields    = "\x1b[33m"
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

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// logRecord 保留业务字段的数字精度及原始 JSON，消息允许空字符串。
type logRecord struct {
	Timestamp  string         `json:"timestamp"`
	Level      string         `json:"level"`
	Project    string         `json:"project"`
	InstanceID string         `json:"instance_id"`
	Component  string         `json:"component"`
	Message    *string        `json:"message"`
	Fields     map[string]any `json:"fields"`
	raw        string
}

func parseRecord(line []byte) (logRecord, bool) {
	var record logRecord
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.UseNumber()
	if err := decoder.Decode(&record); err != nil {
		return record, false
	}
	// Decoder 读取单个值，检查尾部确保整行都是一条有效日志。
	if len(bytes.TrimSpace(line[decoder.InputOffset():])) != 0 {
		return record, false
	}
	if record.Timestamp == "" || record.Project == "" || record.InstanceID == "" ||
		record.Component == "" || record.Message == nil || !contains(levels, record.Level) {
		return record, false
	}
	record.raw = string(bytes.TrimSpace(line))
	return record, true
}

func (record logRecord) fieldsJSON() string {
	if len(record.Fields) == 0 {
		return ""
	}
	var text bytes.Buffer
	encoder := json.NewEncoder(&text)
	encoder.SetEscapeHTML(false)
	// 字段均来自 JSON 解码，值仅包含可编码的 JSON 类型。
	_ = encoder.Encode(record.Fields)
	return strings.TrimSuffix(text.String(), "\n")
}

func (opts options) matches(record logRecord) bool {
	if contains(opts.excludedLevels, record.Level) {
		return false
	}
	for _, condition := range []struct {
		values []string
		value  string
	}{
		{
			values: opts.projects,
			value:  record.Project,
		},
		{
			values: opts.instances,
			value:  record.InstanceID,
		},
		{
			values: opts.components,
			value:  record.Component,
		},
		{
			values: opts.levels,
			value:  record.Level,
		},
	} {
		if len(condition.values) > 0 && !contains(condition.values, condition.value) {
			return false
		}
	}
	if len(opts.keywords) == 0 && len(opts.patterns) == 0 {
		return true
	}
	message := *record.Message
	fields := record.fieldsJSON()
	keywordMessage := message
	keywordFields := fields
	if opts.ignoreKeywordCase {
		keywordMessage = strings.ToLower(message)
		keywordFields = strings.ToLower(fields)
	}
	for _, keyword := range opts.keywords {
		if !strings.Contains(keywordMessage, keyword) && !strings.Contains(keywordFields, keyword) {
			return false
		}
	}
	for _, pattern := range opts.patterns {
		if !pattern.MatchString(message) && (fields == "" || !pattern.MatchString(fields)) {
			return false
		}
	}
	return true
}

func escaped(value string) string {
	quoted := strconv.Quote(value)
	return quoted[1 : len(quoted)-1]
}

func colourText(text string, style string) string {
	return style + text + colourReset
}

func (record logRecord) format(asJSON bool, colour bool) string {
	if asJSON {
		return record.raw
	}
	// 可读时间统一按 UTC 显示到秒，无法解析时保留转义后的原文供排查。
	timestamp := escaped(record.Timestamp)
	if at, err := time.Parse(time.RFC3339Nano, record.Timestamp); err == nil {
		timestamp = at.UTC().Format("2006-01-02 15:04:05")
	}
	// 等级先按可见字符补齐宽度，再添加 ANSI 样式，保持各行列对齐。
	level := fmt.Sprintf("%-9s", strings.ToUpper(escaped(record.Level)))
	project := escaped(record.Project)
	instance := escaped(record.InstanceID)
	component := escaped(record.Component)
	message := escaped(*record.Message)
	fields := record.fieldsJSON()
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
	return text
}

func (opts options) journalArgs() []string {
	args := []string{
		"--no-pager",
		"--output=cat",
		"--all",
		"--since=" + opts.since,
	}
	if opts.until != "" {
		args = append(args, "--until="+opts.until)
	}
	if opts.follow {
		return append(args, "--follow", "--no-tail")
	}
	args = append(args, "--lines=all")
	if opts.lines > 0 {
		args = append(args, "--reverse")
	}
	return args
}

// filterLogs 的 complete 表示已读到 EOF；达到匹配上限时由调用方停止日志进程。
func filterLogs(input io.Reader, output io.Writer, opts options) (complete bool, err error) {
	reader := bufio.NewReader(input)
	var recent []logRecord
	count := 0
	for {
		// 单条日志按需分配内存，支持大于 Scanner 默认 64 KiB 的业务字段。
		line, readErr := reader.ReadBytes('\n')
		if record, valid := parseRecord(line); valid && opts.matches(record) {
			if !opts.follow && opts.lines > 0 {
				recent = append(recent, record)
			} else if _, err := fmt.Fprintln(output, record.format(opts.asJSON, opts.colour)); err != nil {
				return false, err
			}
			count++
			if opts.lines > 0 && count >= opts.lines {
				break
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				return false, readErr
			}
			complete = true
			break
		}
	}
	for i := len(recent) - 1; i >= 0; i-- {
		if _, err := fmt.Fprintln(output, recent[i].format(opts.asJSON, opts.colour)); err != nil {
			return false, err
		}
	}
	return complete, nil
}

func query(ctx context.Context, opts options, output, diagnostics io.Writer) error {
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	command := exec.CommandContext(childCtx, "journalctl", opts.journalArgs()...)
	command.Stderr = diagnostics
	stdout, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	if err := command.Start(); err != nil {
		_ = stdout.Close()
		return err
	}
	complete, readErr := filterLogs(stdout, output, opts)
	if !complete || readErr != nil {
		cancel()
	}
	waitErr := command.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if readErr != nil {
		return readErr
	}
	if complete && waitErr != nil {
		return fmt.Errorf("journalctl 查询失败：%w", waitErr)
	}
	return nil
}
