package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/mctxyz/syslogger/internal/journalwire"
)

// journalRange 保存一次查询中可读记录的首尾时间；空空间的两个端点均为 nil。
type journalRange struct {
	Namespace  string     `json:"namespace"`
	FirstEntry *time.Time `json:"first_entry"`
	LastEntry  *time.Time `json:"last_entry"`
}

// journalBoundary 只解码一条 journal 元数据，时间口径与日志查询的 -s/-u 一致。
func journalBoundary(ctx context.Context, namespace string, latest bool, diagnostics io.Writer) (*time.Time, error) {
	args := []string{
		"--no-pager",
		"--namespace=" + namespace,
		"--output=json",
		"--output-fields=__REALTIME_TIMESTAMP",
	}
	if latest {
		args = append(args, "--reverse", "--lines=1")
	} else {
		// 沿用 systemd 245 的基础参数，从头读取首条后取消，避免遍历历史。
		args = append(args, "--lines=all")
	}
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	command := exec.CommandContext(childCtx, "journalctl", args...)
	command.Stderr = diagnostics
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		_ = stdout.Close()
		return nil, err
	}
	var entry struct {
		Realtime string `json:"__REALTIME_TIMESTAMP"`
	}
	readErr := json.NewDecoder(stdout).Decode(&entry)
	earlyStop := readErr == nil && !latest
	if earlyStop || (readErr != nil && !errors.Is(readErr, io.EOF)) {
		cancel()
	}
	waitErr := command.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, fmt.Errorf("解码 journal 元数据失败：%w", readErr)
	}
	// 主动早停可能返回取消或 SIGKILL，真实的非零退出仍作为查询错误。
	expectedStop := earlyStop && errors.Is(waitErr, context.Canceled)
	var exitErr *exec.ExitError
	if earlyStop && errors.As(waitErr, &exitErr) {
		status, ok := exitErr.Sys().(syscall.WaitStatus)
		expectedStop = ok && status.Signaled() && status.Signal() == syscall.SIGKILL
	}
	if waitErr != nil && !expectedStop {
		return nil, fmt.Errorf("journalctl 查询失败：%w", waitErr)
	}
	if errors.Is(readErr, io.EOF) {
		return nil, nil
	}
	micros, err := strconv.ParseInt(entry.Realtime, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("journal 时间戳 %q 无效：%w", entry.Realtime, err)
	}
	at := time.UnixMicro(micros).UTC()
	return &at, nil
}

// showRanges 每次重新读取四空间，查询完成后统一输出，失败时不输出部分概览。
func showRanges(ctx context.Context, asJSON, colour bool, output, diagnostics io.Writer) error {
	ranges := make([]journalRange, 0, journalwire.GroupCount)
	for group := journalwire.DebugGroup; group < journalwire.GroupCount; group++ {
		current := journalRange{Namespace: group.Namespace()}
		first, err := journalBoundary(ctx, current.Namespace, false, diagnostics)
		if err != nil {
			return fmt.Errorf("%s 最早记录：%w", current.Namespace, err)
		}
		if first != nil {
			last, err := journalBoundary(ctx, current.Namespace, true, diagnostics)
			if err != nil {
				return fmt.Errorf("%s 最新记录：%w", current.Namespace, err)
			}
			// 两次读取间可能发生清理，以最后一次观察为空的结果展示该空间。
			if last != nil {
				current.FirstEntry, current.LastEntry = first, last
			}
		}
		ranges = append(ranges, current)
	}
	if asJSON {
		return json.NewEncoder(output).Encode(ranges)
	}
	// 先对齐纯文本，再为固定空间名称着色，避免 ANSI 序列被计入列宽。
	var table strings.Builder
	writer := tabwriter.NewWriter(&table, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "NAMESPACE\tFIRST_ENTRY\tLAST_ENTRY")
	for _, current := range ranges {
		first, last := "-", "-"
		if current.FirstEntry != nil {
			first = current.FirstEntry.Local().Format("2006-01-02 15:04:05 -0700")
			last = current.LastEntry.Local().Format("2006-01-02 15:04:05 -0700")
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\n", current.Namespace, first, last)
	}
	writer.Flush()
	text := table.String()
	if colour {
		for group := journalwire.DebugGroup; group < journalwire.GroupCount; group++ {
			namespace := group.Namespace()
			text = strings.Replace(text, namespace, colourText(namespace, levelColours[group.Name()]), 1)
		}
	}
	_, err := fmt.Fprint(output, text)
	return err
}
