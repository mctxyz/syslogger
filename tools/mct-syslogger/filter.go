package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
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
	if record.stats != nil {
		record.stats.baseMatches++
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
func filterLogs(input io.Reader, output io.Writer, opts options, stats *queryStats) (complete bool, err error) {
	reader := bufio.NewReader(input)
	var recent []string
	count := 0
	for {
		var phase time.Time
		if stats != nil {
			phase = time.Now()
		}
		// 单条日志按需分配内存，支持大于 Scanner 默认 64 KiB 的业务字段。
		line, readErr := reader.ReadBytes('\n')
		if stats != nil {
			stats.readTime += time.Since(phase)
			if len(line) > 0 {
				stats.scannedLines++
				stats.scannedBytes += int64(len(line))
				if stats.scannedLines == 1 {
					stats.firstLine = time.Since(stats.started)
				}
			}
			phase = time.Now()
		}
		record, valid := parseRecord(line)
		if stats != nil {
			stats.parseTime += time.Since(phase)
		}
		if valid {
			record.stats = stats
			if stats != nil {
				stats.validRecords++
				phase = time.Now()
			}
			matched, err := opts.matches(&record)
			if stats != nil {
				stats.filterTime += time.Since(phase)
			}
			if err != nil {
				return false, err
			}
			if matched {
				if stats != nil {
					stats.matched++
					if stats.matched == 1 {
						stats.firstMatch = time.Since(stats.started)
					}
					phase = time.Now()
				}
				text, err := record.format(opts.asJSON, opts.colour)
				if stats != nil {
					stats.formatTime += time.Since(phase)
				}
				if err != nil {
					return false, err
				}
				if !opts.follow && opts.lines > 0 {
					recent = append(recent, text)
				} else if err := writeLog(output, text, stats); err != nil {
					return false, err
				}
				count++
				if opts.lines > 0 && count >= opts.lines {
					if stats != nil {
						stats.stopReason = "limit"
					}
					break
				}
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				return false, readErr
			}
			complete = true
			if stats != nil {
				stats.stopReason = "eof"
			}
			break
		}
	}
	for i := len(recent) - 1; i >= 0; i-- {
		if err := writeLog(output, recent[i], stats); err != nil {
			return false, err
		}
	}
	return complete, nil
}

// writeLog 统一统计流式和最近 N 条缓存输出的耗时，仅完整写入后累计输出行数。
func writeLog(output io.Writer, text string, stats *queryStats) error {
	var phase time.Time
	if stats != nil {
		phase = time.Now()
	}
	_, err := fmt.Fprintln(output, text)
	if stats != nil {
		stats.writeTime += time.Since(phase)
		if err == nil {
			stats.written++
			if stats.written == 1 {
				stats.firstOutput = time.Since(stats.started)
			}
		}
	}
	return err
}
