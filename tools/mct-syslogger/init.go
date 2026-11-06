package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/mctxyz/syslogger/internal/hostinit"
)

func parseInitOptions(args []string, help io.Writer) (hostinit.Options, error) {
	var opts hostinit.Options
	flags := flag.NewFlagSet("mct-syslogger init", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&opts.User, "user", "", "允许写名称目录的已有运行账户 `USER`（必填）")
	flags.StringVar(&opts.JournalMaxUse, "journal-max-use", hostinit.DefaultJournalMaxUse, "专用 journal 持久化总容量 `SIZE`，正整数及可选 K/M/G/T/P/E 单位")
	flags.Usage = func() {
		fmt.Fprint(help, `mct-syslogger init — 初始化本机名称目录及专用 journal

用法：
  sudo mct-syslogger init --user appuser [--journal-max-use 5G]
  mct-syslogger init --help

参数：
  --user USER             指定已有运行账户，仅 root 和该账户可写名称目录
  --journal-max-use SIZE  持久化 journal 总容量，默认 5G；按 1024 进制计量
  -h, --help             显示本子命令帮助

执行时逐步输出命令及参数、目录创建或复用、属主权限设置、配置写入和旧受管配置清理。
命令在执行前记录，文件操作在成功后记录；全部步骤完成后才输出最终摘要。
初始化需要 Linux、systemd 245+、root，以及 getent、groupadd、usermod、systemctl、systemd-analyze。
账户和写入组须由本机 files 管理；默认 files systemd 可用，外部身份源的额外授权由管理员核验。
首次迁移前先停止旧版业务进程；命令保留日志、清单及锁文件，修复目录权限。
名称目录使用 mct-syslogger 组，目录为 2775，JSON 和锁为 0664；额外组成员或 ACL 会报错。
专用 journal 开启持久化与原生透明压缩，/run 容量保持系统现有配置。
245–247 使用受管命名空间主文件，248+ 使用受管配置片段；既有非受管文件保留。
每次执行都会应用配置并重启专用 journald 实例，不更改默认系统 journal。
重复执行时省略容量参数仍使用 5G；缩小容量可能使 journald 清理旧归档。
容量不是严格硬配额，实际占用可能受活动文件及磁盘剩余空间影响。
已有管理员独立配置保留；覆盖受管设置的冲突会明确报错。
完成后重新登录或重启业务服务以刷新组身份；本命令不授予额外日志读取权限。
`)
	}
	if err := flags.Parse(args); err != nil {
		return opts, err
	}
	if flags.NArg() != 0 {
		return opts, fmt.Errorf("init 不接受位置参数")
	}
	return opts, opts.Validate()
}

func initCode(args []string) int {
	opts, err := parseInitOptions(args, os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "mct-syslogger init:", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := hostinit.Run(ctx, opts, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "mct-syslogger init:", err)
		if ctx.Err() != nil {
			return 130
		}
		return 1
	}
	return 0
}
