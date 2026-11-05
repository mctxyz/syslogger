package hostinit

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"strings"

	"github.com/mctxyz/syslogger/internal/catalog"
	"github.com/mctxyz/syslogger/internal/journalwire"
)

// Run 仅在显式的 root 初始化操作中修改专用资源，不操作默认 journal。
func Run(ctx context.Context, opts Options, output io.Writer) error {
	if err := opts.Validate(); err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("初始化需要 root，请使用 sudo mct-syslogger init --user %s", opts.User)
	}
	for _, name := range []string{"getent", "groupadd", "usermod", "systemctl", "systemd-analyze"} {
		if _, err := exec.LookPath(name); err != nil {
			return fmt.Errorf("初始化需要系统工具 %s: %w", name, err)
		}
	}
	version, err := installedSystemdVersion(ctx)
	if err != nil {
		return err
	}
	for _, suffix := range []string{"socket", "service"} {
		unit := "systemd-journald@" + journalwire.Namespace + "." + suffix
		state, err := command(ctx, "systemctl", "show", "--property=LoadState", "--value", unit)
		if err != nil {
			return err
		}
		if strings.TrimSpace(state) != "loaded" {
			return fmt.Errorf("无法加载 %s，需要支持 journal namespaces 的 systemd 245+ 主机", unit)
		}
	}
	account, err := user.Lookup(opts.User)
	if err != nil {
		return fmt.Errorf("查找运行账户: %w", err)
	}
	gid, err := prepareGroup(ctx, account)
	if err != nil {
		return fmt.Errorf("准备目录写入组: %w", err)
	}
	if err := prepareCatalog(catalog.Root, gid); err != nil {
		return fmt.Errorf("准备名称目录: %w", err)
	}
	if err := prepareJournal(ctx, opts.JournalMaxUse, version); err != nil {
		return fmt.Errorf("准备专用 journal: %w", err)
	}
	_, err = fmt.Fprintf(output, "初始化完成：目录 %s，写入组 %s，账户 %s；journal %s 持久化容量 %s，压缩开启。\n请重新登录或重启业务服务以刷新组身份。\n", catalog.Root, catalog.Group, account.Username, journalwire.Namespace, opts.JournalMaxUse)
	return err
}

// command 使用独立 argv 调用系统能力，固定诊断输出格式以核对配置来源。
func command(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "SYSTEMD_COLORS=0", "SYSTEMD_PAGER=cat", "LC_ALL=C")
	data, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s 执行失败: %w: %s", name, err, strings.TrimSpace(string(data)))
	}
	return string(data), nil
}
