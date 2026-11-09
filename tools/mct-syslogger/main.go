// Command mct-syslogger 展示日志名称目录、空间留存范围并检索本机 syslogger 日志。
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

// version 由发布构建通过 -ldflags -X main.version 注入 tag，普通源码构建标记为 dev。
var version = "dev"

func mainCode() int {
	if len(os.Args) > 1 && os.Args[1] == "init" {
		return initCode(os.Args[2:])
	}
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
	if opts.version {
		if _, err := fmt.Fprintf(os.Stdout, "mct-syslogger %s\n", version); err != nil {
			fmt.Fprintln(os.Stderr, "mct-syslogger:", err)
			return 1
		}
		return 0
	}
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
	if opts.ranges {
		err = showRanges(ctx, opts.asJSON, opts.colour, os.Stdout, os.Stderr)
	} else {
		err = query(ctx, opts, os.Stdout, os.Stderr)
	}
	if err != nil {
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
