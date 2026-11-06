package hostinit

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"

	"github.com/mctxyz/syslogger/internal/catalog"
	"github.com/mctxyz/syslogger/internal/journalwire"
)

// initializer 将单次初始化的命令上下文和过程输出绑定到同一个调用。
type initializer struct {
	ctx    context.Context
	logger *log.Logger
}

// Run 仅在显式的 root 初始化操作中修改专用资源，不操作默认 journal。
func Run(ctx context.Context, opts Options, output io.Writer) error {
	if err := opts.Validate(); err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("初始化需要 root，请使用 sudo mct-syslogger init")
	}
	for _, name := range []string{"systemctl", "systemd-analyze"} {
		if _, err := exec.LookPath(name); err != nil {
			return fmt.Errorf("初始化需要系统工具 %s: %w", name, err)
		}
	}
	init := initializer{ctx: ctx, logger: log.New(output, "", 0)}
	init.logger.Printf("[开始初始化] 目录所有者=%q", catalog.User)
	version, err := init.installedSystemdVersion()
	if err != nil {
		return err
	}
	for group := journalwire.DebugGroup; group < journalwire.GroupCount; group++ {
		init.logger.Printf("[等级空间] namespace=%q 持久化容量=%q", group.Namespace(), opts.JournalMaxUse[group])
		for _, suffix := range []string{"socket", "service"} {
			unit := "systemd-journald@" + group.Namespace() + "." + suffix
			state, err := init.command("systemctl", "show", "--property=LoadState", "--value", unit)
			if err != nil {
				return err
			}
			if strings.TrimSpace(state) != "loaded" {
				return fmt.Errorf("无法加载 %s，需要支持 journal namespaces 的 systemd 245+ 主机", unit)
			}
		}
	}
	uid, gid, err := init.prepareAccount()
	if err != nil {
		return err
	}
	if err := init.prepareCatalog(catalog.Root, uid, gid); err != nil {
		return fmt.Errorf("准备名称目录: %w", err)
	}
	for group := journalwire.DebugGroup; group < journalwire.GroupCount; group++ {
		if err := init.prepareJournal(group, opts.JournalMaxUse[group], version); err != nil {
			return fmt.Errorf("准备专用 journal %s: %w", group.Namespace(), err)
		}
	}
	if _, err := fmt.Fprintf(output, "初始化完成：目录 %s，所有者 %s（UID=%d GID=%d）；四空间持久化与压缩开启。\n", catalog.Root, catalog.User, uid, gid); err != nil {
		return err
	}
	for group := journalwire.DebugGroup; group < journalwire.GroupCount; group++ {
		if _, err := fmt.Fprintf(output, "  %s：%s\n", group.Namespace(), opts.JournalMaxUse[group]); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintln(output, "名称目录权限为 0755，清单和锁文件为 0644。")
	return err
}

// command 使用独立 argv 调用系统能力，固定诊断输出格式以核对配置来源。
func (init *initializer) command(name string, args ...string) (string, error) {
	cmd := exec.CommandContext(init.ctx, name, args...)
	cmd.Env = append(os.Environ(), "SYSTEMD_COLORS=0", "SYSTEMD_PAGER=cat", "LC_ALL=C")
	init.logger.Printf("[执行命令] %q 参数=%q", cmd.Path, args)
	data, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s 执行失败: %w: %s", name, err, strings.TrimSpace(string(data)))
	}
	return string(data), nil
}
