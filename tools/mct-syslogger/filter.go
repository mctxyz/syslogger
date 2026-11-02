package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

func (opts options) matches(record *logRecord) (bool, error) {
	if !contains(opts.levels, record.Level) {
		return false, nil
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
			return false, nil
		}
	}
	message := *record.Message
	keywordMessage := message
	if len(opts.keywords) > 0 && opts.ignoreKeywordCase {
		keywordMessage = strings.ToLower(message)
	}
	var keywordFields *string
	for _, keyword := range opts.keywords {
		if strings.Contains(keywordMessage, keyword) {
			continue
		}
		if keywordFields == nil {
			fields, err := record.fieldsJSON()
			if err != nil {
				return false, err
			}
			if opts.ignoreKeywordCase {
				fields = strings.ToLower(fields)
			}
			keywordFields = &fields
		}
		if !strings.Contains(*keywordFields, keyword) {
			return false, nil
		}
	}
	for _, pattern := range opts.patterns {
		if pattern.MatchString(message) {
			continue
		}
		fields, err := record.fieldsJSON()
		if err != nil {
			return false, err
		}
		if fields == "" || !pattern.MatchString(fields) {
			return false, nil
		}
	}
	return true, nil
}

// filterLogs 的 complete 表示已读到 EOF；达到匹配上限时由调用方停止日志进程。
func filterLogs(input io.Reader, output io.Writer, opts options) (complete bool, err error) {
	reader := bufio.NewReader(input)
	var recent []string
	count := 0
	for {
		// 单条日志按需分配内存，支持大于 Scanner 默认 64 KiB 的业务字段。
		line, readErr := reader.ReadBytes('\n')
		if record, valid := parseRecord(line); valid {
			matched, err := opts.matches(&record)
			if err != nil {
				return false, err
			}
			if matched {
				text, err := record.format(opts.asJSON, opts.colour)
				if err != nil {
					return false, err
				}
				if !opts.follow && opts.lines > 0 {
					recent = append(recent, text)
				} else if _, err := fmt.Fprintln(output, text); err != nil {
					return false, err
				}
				count++
				if opts.lines > 0 && count >= opts.lines {
					break
				}
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
		if _, err := fmt.Fprintln(output, recent[i]); err != nil {
			return false, err
		}
	}
	return complete, nil
}
