package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"time"

	"github.com/mctxyz/syslogger/internal/journalwire"
)

func (opts options) journalArgs() []string {
	var selected [journalwire.GroupCount]bool
	for severity, name := range levels {
		if contains(opts.levels, name) {
			selected[journalwire.GroupForSeverity(uint8(severity))] = true
		}
	}
	// 最终等级只落入一组时直接选源；多组由原生 journal 合并，空集合保留查询错误反馈。
	namespace := "*"
	groupCount := 0
	for group := journalwire.DebugGroup; group < journalwire.GroupCount; group++ {
		if selected[group] {
			namespace = group.Namespace()
			groupCount++
		}
	}
	if groupCount != 1 {
		namespace = "*"
	}
	args := []string{
		"--no-pager",
		"--namespace=" + namespace,
		"--output=cat",
		"--all",
		"--since=" + opts.since,
	}
	// 原生 namespace 字段限制返回范围；空等级仍保留空间边界，再由 PRIORITY 表达无结果。
	for group := journalwire.DebugGroup; group < journalwire.GroupCount; group++ {
		if selected[group] || len(opts.levels) == 0 {
			args = append(args, "_NAMESPACE="+group.Namespace())
		}
	}
	if opts.until != "" {
		args = append(args, "--until="+opts.until)
	}
	if opts.follow {
		args = append(args, "--follow", "--no-tail")
	} else {
		args = append(args, "--lines=all")
		if opts.lines > 0 {
			args = append(args, "--reverse")
		}
	}
	// 原文身份字段直接参与索引；相同字段取 OR，不同字段取 AND。
	for _, selector := range []struct {
		field  string
		values []string
	}{
		{journalwire.ProjectField, opts.projects},
		{journalwire.InstanceField, opts.instances},
		{journalwire.ComponentField, opts.components},
	} {
		for _, value := range selector.values {
			args = append(args, selector.field+"="+value)
		}
	}
	if len(opts.levels) == 0 {
		// SDK 只发送 severity 0～7；用不存在的值表达空集合，同时保留 journalctl 的校验与错误反馈。
		args = append(args, "PRIORITY="+strconv.Itoa(len(levels)))
	} else if len(opts.levels) < len(levels) {
		for severity, name := range levels {
			if contains(opts.levels, name) {
				args = append(args, "PRIORITY="+strconv.Itoa(severity))
			}
		}
	}
	return args
}

func query(ctx context.Context, opts options, output, diagnostics io.Writer) (queryErr error) {
	args := opts.journalArgs()
	var stats *queryStats
	if opts.debug {
		stats = &queryStats{started: time.Now()}
		defer func() {
			outcome := stats.stopReason
			if errors.Is(queryErr, context.Canceled) || errors.Is(queryErr, context.DeadlineExceeded) {
				outcome = "canceled"
			} else if queryErr != nil {
				outcome = "error"
			}
			if err := stats.report(diagnostics, args, outcome); err != nil {
				queryErr = errors.Join(queryErr, err)
			}
		}()
	}
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	command := exec.CommandContext(childCtx, "journalctl", args...)
	command.Stderr = diagnostics
	stdout, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	var phase time.Time
	if stats != nil {
		phase = time.Now()
	}
	startErr := command.Start()
	if stats != nil {
		stats.startTime = time.Since(phase)
	}
	if startErr != nil {
		_ = stdout.Close()
		return startErr
	}
	complete, readErr := filterLogs(stdout, output, opts, stats)
	if stats != nil {
		phase = time.Now()
	}
	if !complete || readErr != nil {
		cancel()
	}
	waitErr := command.Wait()
	if stats != nil {
		stats.waitTime = time.Since(phase)
	}
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
