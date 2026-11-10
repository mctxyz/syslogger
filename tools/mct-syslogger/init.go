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
	// 初始化输出保持纯文本，统一接受显示修饰项而不向主机配置传递。
	flags.Bool("colour", false, "接受显示开关，初始化输出保持纯文本")
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
  --colour                        接受显示开关，初始化输出保持纯文本
  -h, --help                      显示本子命令帮助

执行时逐步输出命令及参数、目录创建或复用、新目录属主权限设置及当前配置写入。
命令在执行前记录，文件操作在成功后记录；全部步骤完成后才输出最终摘要。
初始化需要 Linux、systemd 245+、root，以及 systemctl、systemd-analyze、groupadd。
名称根目录不存在时创建，已有目录复用；共享组不存在时创建，已有组保留成员。
根目录及已有 hash 项目目录修正为 root:mct-syslogger、2775。
已有 hash JSON 和稳定锁只修正属组及 0664，保留原 UID、内容和 inode，不删除重建。
无关对象和临时项保持原样；受管对象的链接、特殊类型、多重硬链接或 ACL 异常会报错。
权限迁移前停止相关写入程序；失败保留已完成步骤，修正异常后可重复执行。
管理员将各程序账户加入 mct-syslogger 组，重启对应程序以刷新组身份：
  sudo usermod -aG mct-syslogger username
将 username 替换为实际程序账户；交互账户退出并重新登录后生效。
SDK 新建项目目录为 2775，JSON 和稳定锁为 0664，属组继承根目录，初始保留创建者 UID。
后续 init 将已有项目目录统一为 root 所有；正确的元数据直接复用，不重复修改。
组内账户共同维护名称目录；下级对象改为 root 所有时，保持属组和权限即可继续使用。
专用 journal 开启持久化与原生透明压缩，/run 容量保持系统现有配置。
245–247 使用受管命名空间主文件，248+ 使用受管配置片段；既有非受管文件保留。
每次执行都会应用配置并重启四个专用 journald 实例，不更改默认系统 journal。
容量接受正整数字节及 K/M/G/T/P/E 单位，按 1024 进制计量，默认合计 5 GiB。
重复执行时省略某项仍使用该项默认容量；缩小容量可能使对应空间清理旧归档。
容量不是严格硬配额，实际占用可能受活动文件及磁盘剩余空间影响。
已有管理员独立配置保留；覆盖受管设置的冲突会明确报错。
等级由 SDK 路由；init 检查 MaxLevelStore 不会过滤目标等级，不生成该设置。
本命令只准备共享组，不创建业务账户或修改组成员；systemd-journal 读取权限另行管理。
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
