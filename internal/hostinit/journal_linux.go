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
	directory, err := init.openConfigDirectory(filepath.Dir(path), true)
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

func (init *initializer) openConfigDirectory(path string, create bool) (*os.File, error) {
	directory, err := init.openDirectory(path, create, 0755)
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

// managedConfigFile 通过受保护目录 fd 检查文件归属，供写入和版本迁移清理共用。
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

func (init *initializer) removeManagedConfig(path string) error {
	directory, err := init.openConfigDirectory(filepath.Dir(path), false)
	if errors.Is(err, unix.ENOENT) {
		init.logger.Printf("[无需清理] %q 不存在", path)
		return nil
	}
	if err != nil {
		return err
	}
	defer directory.Close()
	found, owned, err := managedConfigFile(directory, filepath.Base(path))
	if err != nil {
		return err
	}
	if !found {
		init.logger.Printf("[无需清理] %q 不存在", path)
		return nil
	}
	if !owned {
		init.logger.Printf("[保留配置] %q 非本工具受管文件", path)
		return nil
	}
	if err := unix.Unlinkat(int(directory.Fd()), filepath.Base(path), 0); err != nil {
		return err
	}
	init.logger.Printf("[清理旧配置] %q", path)
	return nil
}

func (init *initializer) prepareJournal(maxUse string, version int) error {
	mainPath := filepath.Join("/etc/systemd", journalBaseName)
	dropinPath := filepath.Join(mainPath+".d", configName)
	target, previous := dropinPath, mainPath
	if version < 248 {
		target, previous = mainPath, dropinPath
	}
	init.logger.Printf("[配置路径] systemd=%d 使用 %q", version, target)
	if err := init.writeJournalConfig(target, journalConfig(maxUse)); err != nil {
		return err
	}
	sources, err := journalConfigSources([]string{"/etc/systemd", "/run/systemd", "/usr/local/lib/systemd", "/usr/lib/systemd", "/lib/systemd"}, version)
	if err != nil {
		return err
	}
	for _, source := range sources {
		init.logger.Printf("[读取配置] %q", source.path)
	}
	if err := checkJournalConfig(sources, journalSettings(maxUse)); err != nil {
		return err
	}
	if err := init.removeManagedConfig(previous); err != nil {
		return err
	}
	// 删除旧主文件可能使较低优先级主文件显露，重启前核验最终实际集合。
	sources, err = journalConfigSources([]string{"/etc/systemd", "/run/systemd", "/usr/local/lib/systemd", "/usr/lib/systemd", "/lib/systemd"}, version)
	if err != nil {
		return err
	}
	for _, source := range sources {
		init.logger.Printf("[读取配置] %q", source.path)
	}
	if err := checkJournalConfig(sources, journalSettings(maxUse)); err != nil {
		return err
	}
	return init.activateJournal()
}

// activateJournal 每次都应用配置，覆盖上次在写入后、应用前中断的情况。
func (init *initializer) activateJournal() error {
	socket := "systemd-journald@" + journalwire.Namespace + ".socket"
	service := "systemd-journald@" + journalwire.Namespace + ".service"
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
	info, err := os.Lstat(journalwire.SocketPath)
	if err != nil {
		return fmt.Errorf("检查专用日志入口: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("专用日志入口 %s 不是 socket", journalwire.SocketPath)
	}
	init.logger.Printf("[检查日志入口] %q 为 Unix socket", journalwire.SocketPath)
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
