package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// referenceRecord 固定优化前的完整解码、匹配和输出规则，作为差分测试与基准的独立对照。
type referenceRecord struct {
	Timestamp  string         `json:"timestamp"`
	Level      string         `json:"level"`
	Project    string         `json:"project"`
	InstanceID string         `json:"instance_id"`
	Component  string         `json:"component"`
	Message    *string        `json:"message"`
	Fields     map[string]any `json:"fields"`
	raw        string
}

func parseReferenceRecord(line []byte) (referenceRecord, bool) {
	var record referenceRecord
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.UseNumber()
	if err := decoder.Decode(&record); err != nil {
		return record, false
	}
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

func (record referenceRecord) fieldsJSON() string {
	if len(record.Fields) == 0 {
		return ""
	}
	var text bytes.Buffer
	encoder := json.NewEncoder(&text)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(record.Fields)
	return strings.TrimSuffix(text.String(), "\n")
}

func (record referenceRecord) matches(opts options) bool {
	if !contains(opts.levels, record.Level) {
		return false
	}
	for _, condition := range []struct {
		values []string
		value  string
	}{
		{values: opts.projects, value: record.Project},
		{values: opts.instances, value: record.InstanceID},
		{values: opts.components, value: record.Component},
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

func (record referenceRecord) format(asJSON, colour bool) string {
	if asJSON {
		return record.raw
	}
	timestamp := escaped(record.Timestamp)
	if at, err := time.Parse(time.RFC3339Nano, record.Timestamp); err == nil {
		timestamp = at.Local().Format("2006-01-02 15:04:05")
	}
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
	text := fmt.Sprintf("%s %s %s/%s/%s %s", timestamp, level, project, instance, component, message)
	if fields != "" {
		text += " " + fields
	}
	return text
}

func referenceFilterLogs(input io.Reader, output io.Writer, opts options) (complete bool, err error) {
	reader := bufio.NewReader(input)
	var recent []referenceRecord
	count := 0
	for {
		line, readErr := reader.ReadBytes('\n')
		if record, valid := parseReferenceRecord(line); valid && record.matches(opts) {
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
