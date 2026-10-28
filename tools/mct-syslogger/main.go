// Command mct-syslogger 展示日志名称目录并检索本机 journal 中的 syslogger 日志。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/mctxyz/syslogger/internal/catalog"
)

func mainCode() int {
	opts, err := parseOptions(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "mct-syslogger:", err)
		return 2
	}
	// 下游管道关闭后通过写入错误完成收尾，目录和日志查询采用一致的退出语义。
	signal.Ignore(syscall.SIGPIPE)
	if opts.list {
		if err := showCatalog(catalog.Store{Root: catalog.Root}, opts, os.Stdout); err != nil {
			if errors.Is(err, syscall.EPIPE) {
				return 0
			}
			fmt.Fprintln(os.Stderr, "mct-syslogger:", err)
			return 1
		}
		return 0
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := query(ctx, opts, os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, context.Canceled) {
			return 130
		}
		if errors.Is(err, syscall.EPIPE) {
			return 0
		}
		fmt.Fprintln(os.Stderr, "mct-syslogger:", err)
		return 1
	}
	return 0
}

func main() {
	os.Exit(mainCode())
}
