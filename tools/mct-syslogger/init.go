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
	"github.com/mctxyz/syslogger/internal/journalwire"
)

func parseInitOptions(args []string, help io.Writer) (hostinit.Options, error) {
	var opts hostinit.Options
	flags := flag.NewFlagSet("mct-syslogger init", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	for group := journalwire.DebugGroup; group < journalwire.GroupCount; group++ {
		flags.StringVar(&opts.JournalMaxUse[group], "journal-"+group.Name()+"-max-use", hostinit.DefaultJournalMaxUse(group), "该等级空间的持久化容量 `SIZE`，正整数及可选 K/M/G/T/P/E 单位")
	}
	flags.Usage = func() {
		fmt.Fprint(help, `mct-syslogger init — 初始化本机名称目录及四个等级 journal 空间

用法：
  sudo mct-syslogger init [四个空间的容量参数]
  mct-syslogger init --help

参数：
  --journal-debug-max-use SIZE     debug 空间容量，默认 1G
  --journal-info-max-use SIZE      info/notice 空间容量，默认 2G
  --journal-warning-max-use SIZE   warning 空间容量，默认 1G
  --journal-error-max-use SIZE     error 及更严重等级空间容量，默认 1G
  -h, --help                      显示本子命令帮助

执行时逐步输出命令及参数、目录创建或复用、属主权限设置、配置写入和旧受管配置清理。
命令在执行前记录，文件操作在成功后记录；全部步骤完成后才输出最终摘要。
初始化需要 Linux、systemd 245+、root、已有的本机 app 账户，以及 systemctl、systemd-analyze。
首次迁移前先停止旧版业务进程；命令保留日志、清单及锁文件，修复目录权限。
名称目录及其文件固定由 app 及其主 GID 持有；目录为 0755，JSON 和锁为 0644，ACL 会报错。
root 进程使用配套 SDK 时，新项目目录、清单和锁同样继承初始化根目录的所有者。
专用 journal 开启持久化与原生透明压缩，/run 容量保持系统现有配置。
245–247 使用受管命名空间主文件，248+ 使用受管配置片段；既有非受管文件保留。
每次执行都会应用配置并重启四个专用 journald 实例，不更改默认系统 journal。
容量接受正整数字节及 K/M/G/T/P/E 单位，按 1024 进制计量，默认合计 5 GiB。
重复执行时省略某项仍使用该项默认容量；缩小容量可能使对应空间清理旧归档。
容量不是严格硬配额，实际占用可能受活动文件及磁盘剩余空间影响。
已有管理员独立配置保留；覆盖受管设置的冲突会明确报错。
等级由 SDK 路由；init 检查 MaxLevelStore 不会过滤目标等级，不生成该设置。
本命令保留系统账户配置，日志读取权限继续由管理员管理。
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
