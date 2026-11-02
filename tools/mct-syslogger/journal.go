package main

import (
	"context"
	"fmt"
	"io"
	"os/exec"
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
		return append(args, "--follow", "--no-tail")
	}
	args = append(args, "--lines=all")
	if opts.lines > 0 {
		args = append(args, "--reverse")
	}
	return args
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
