package hostinit

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mctxyz/syslogger/internal/journalwire"
	"golang.org/x/sys/unix"
)

const configMarker = "# 由 mct-syslogger init 管理。\n"
const configName = "60-mct-syslogger-init.conf"

func journalSettings(maxUse string) map[string]string {
	return map[string]string{
		"Storage":          "persistent",
		"SystemMaxUse":     maxUse,
		"Compress":         "yes",
		"ForwardToSyslog":  "no",
		"ForwardToKMsg":    "no",
		"ForwardToConsole": "no",
		"ForwardToWall":    "no",
	}
}

func journalConfig(maxUse string) string {
	settings := journalSettings(maxUse)
	keys := make([]string, 0, len(settings))
	for key := range settings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var content strings.Builder
	content.WriteString(configMarker)
	content.WriteString("[Journal]\n")
	for _, key := range keys {
		fmt.Fprintf(&content, "%s=%s\n", key, settings[key])
	}
	return content.String()
}

// writeJournalConfig 只原子替换工具自己生成的片段，保留管理员独立配置。
func (init *initializer) writeJournalConfig(path, content string) error {
	directory, err := init.openConfigDirectory(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	found, owned, err := managedConfigFile(directory, filepath.Base(path))
	if err != nil {
		return err
	}
	if found && !owned {
		return fmt.Errorf("保留已有非受管配置 %s，请管理员先调整", path)
	}
	file, err := os.CreateTemp(directory.Name(), ".mct-syslogger-init-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := io.WriteString(file, content); err != nil {
		return err
	}
	mode := os.FileMode(0644)
	if err := file.Chmod(mode); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	if found {
		init.logger.Printf("[更新配置] %q mode=%04o（临时文件原子替换）", path, mode.Perm())
	} else {
		init.logger.Printf("[创建配置] %q mode=%04o（临时文件原子替换）", path, mode.Perm())
	}
	return nil
}

func (init *initializer) openConfigDirectory(path string) (*os.File, error) {
	directory, _, err := init.openDirectory(path, 0755)
	if err != nil {
		return nil, err
	}
	stat, err := inspect(directory, true)
	if err == nil && (stat.Uid != uint32(os.Geteuid()) || stat.Mode&0022 != 0) {
		err = fmt.Errorf("配置目录 %s 必须由初始化账户持有且仅所有者可写（UID=%d，权限=%#o，当前UID=%d）", path, stat.Uid, stat.Mode&0777, os.Geteuid())
	}
	if err != nil {
		directory.Close()
		return nil, err
	}
	return directory, nil
}

// managedConfigFile 通过受保护目录 fd 识别工具拥有的配置，供幂等原子更新使用。
func managedConfigFile(directory *os.File, name string) (bool, bool, error) {
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	file := os.NewFile(uintptr(fd), filepath.Join(directory.Name(), name))
	defer file.Close()
	stat, err := inspect(file, false)
	if err != nil {
		return false, false, err
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return false, false, fmt.Errorf("配置文件 %s 的所有者不安全", file.Name())
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return false, false, err
	}
	return true, strings.HasPrefix(string(data), configMarker), nil
}

func (init *initializer) prepareJournal(group journalwire.Group, maxUse string, version int) error {
	target := filepath.Join("/etc/systemd", journalBaseName(group))
	if version >= 248 {
		target = filepath.Join(target+".d", configName)
	}
	init.logger.Printf("[配置路径] systemd=%d 使用 %q", version, target)
	if err := init.writeJournalConfig(target, journalConfig(maxUse)); err != nil {
		return err
	}
	sources, err := journalConfigSources([]string{"/etc/systemd", "/run/systemd", "/usr/local/lib/systemd", "/usr/lib/systemd", "/lib/systemd"}, version, group)
	if err != nil {
		return err
	}
	for _, source := range sources {
		init.logger.Printf("[读取配置] %q", source.path)
	}
	if err := checkJournalConfig(sources, journalSettings(maxUse), group); err != nil {
		return err
	}
	return init.activateJournal(group)
}

// activateJournal 每次都应用配置，覆盖上次在写入后、应用前中断的情况。
func (init *initializer) activateJournal(group journalwire.Group) error {
	socket := "systemd-journald@" + group.Namespace() + ".socket"
	service := "systemd-journald@" + group.Namespace() + ".service"
	for _, args := range [][]string{
		{"add-wants", "sockets.target", socket},
		{"start", socket},
		{"restart", service},
		{"is-active", "--quiet", socket},
		{"is-active", "--quiet", service},
	} {
		output, err := init.command("systemctl", args...)
		if err != nil {
			return err
		}
		if args[0] == "add-wants" && strings.TrimSpace(output) != "" {
			init.logger.Printf("[systemctl 输出] %s", strings.TrimSpace(output))
		}
	}
	info, err := os.Lstat(group.SocketPath())
	if err != nil {
		return fmt.Errorf("检查专用日志入口: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("专用日志入口 %s 不是 socket", group.SocketPath())
	}
	init.logger.Printf("[检查日志入口] %q 为 Unix socket", group.SocketPath())
	return nil
}

func sameSetting(key, actual, expected string) bool {
	if key == "SystemMaxUse" {
		a, errA := sizeBytes(actual)
		b, errB := sizeBytes(expected)
		return errA == nil && errB == nil && a == b
	}
	if expected == "yes" || expected == "no" {
		switch strings.ToLower(actual) {
		case "1", "yes", "true", "on":
			return expected == "yes"
		case "0", "no", "false", "off":
			return expected == "no"
		}
	}
	return actual == expected
}
