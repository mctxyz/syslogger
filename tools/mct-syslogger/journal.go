package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"time"

	"github.com/mctxyz/syslogger/internal/identity"
)

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
		args = append(args, "--follow", "--no-tail")
	} else {
		args = append(args, "--lines=all")
		if opts.lines > 0 {
			args = append(args, "--reverse")
		}
	}
	// 模块的 syslog tag 来自项目；同字段的重复 match 由 journalctl 按 OR 组合，不同字段取 AND。
	for _, project := range opts.projects {
		args = append(args, "SYSLOG_IDENTIFIER="+identity.SyslogTag(project))
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
