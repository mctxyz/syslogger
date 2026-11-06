package hostinit

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/mctxyz/syslogger/internal/journalwire"
)

type configSource struct {
	path string
	data string
}

type configValue struct {
	value  string
	source string
}

// journalBaseName 关联等级组与 systemd 主文件、片段目录的名称。
func journalBaseName(group journalwire.Group) string {
	return "journald@" + group.Namespace() + ".conf"
}

func (init *initializer) installedSystemdVersion() (int, error) {
	output, err := init.command("systemd-analyze", "--version")
	if err != nil {
		return 0, err
	}
	version, err := parseSystemdVersion(output)
	if err != nil {
		return 0, err
	}
	init.logger.Printf("[检查 systemd] 版本=%d", version)
	return version, nil
}

func parseSystemdVersion(output string) (int, error) {
	fields := strings.Fields(output)
	if len(fields) < 2 || fields[0] != "systemd" {
		return 0, fmt.Errorf("无法确认已安装的 systemd 版本")
	}
	text := fields[1]
	if end := strings.IndexFunc(text, func(character rune) bool { return character < '0' || character > '9' }); end >= 0 {
		text = text[:end]
	}
	version, err := strconv.Atoi(text)
	if err != nil || version < 245 {
		return 0, fmt.Errorf("初始化需要 systemd 245+，当前版本 %q", fields[1])
	}
	return version, nil
}

// journalConfigSources 按版本和目录优先级选择真实文件，正文注释不参与文件边界识别。
func journalConfigSources(directories []string, version int, group journalwire.Group) ([]configSource, error) {
	var sources []configSource
	baseName := journalBaseName(group)
	mainDirectories := directories
	if version < 256 {
		mainDirectories = directories[:1]
	}
	for _, directory := range mainDirectories {
		path := filepath.Join(directory, baseName)
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		sources = append(sources, configSource{path, string(data)})
		break
	}
	// 245–247 的命名空间只读取主文件，248 才引入命名空间 drop-in。
	if version < 248 {
		return sources, nil
	}
	selected := make(map[string]string)
	for _, directory := range directories {
		path := filepath.Join(directory, baseName+".d")
		entries, err := os.ReadDir(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".conf") {
				continue
			}
			if _, found := selected[name]; !found {
				selected[name] = filepath.Join(path, name)
			}
		}
	}
	names := make([]string, 0, len(selected))
	for name := range selected {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		path := selected[name]
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		sources = append(sources, configSource{path, string(data)})
	}
	return sources, nil
}

// checkJournalConfig 逐文件核对受管键，并在语法异常时拒绝宣称配置已经生效。
func checkJournalConfig(sources []configSource, wanted map[string]string, group journalwire.Group) error {
	values := make(map[string]configValue)
	for _, source := range sources {
		if err := readJournalSettings(source, wanted, values); err != nil {
			return err
		}
	}
	for key, expected := range wanted {
		actual, found := values[key]
		if !found || !sameSetting(key, actual.value, expected) {
			return fmt.Errorf("专用 journal 配置冲突：%s=%q，期望 %q，来源 %s；请管理员调整独立配置后重试", key, actual.value, expected, actual.source)
		}
	}
	if actual, found := values["MaxLevelStore"]; found {
		level, err := storeLevel(actual.value)
		if err != nil {
			return fmt.Errorf("配置 %s: %w", actual.source, err)
		}
		if level < group.MaxSeverity() {
			return fmt.Errorf("专用 journal %s 配置冲突：MaxLevelStore=%q 会丢弃该组日志，来源 %s", group.Namespace(), actual.value, actual.source)
		}
	}
	return nil
}

// storeLevel 只解析官方等级名称与 0～7；无法确认的值交由管理员修正。
func storeLevel(value string) (uint8, error) {
	for level, name := range [...]string{"emerg", "alert", "crit", "err", "warning", "notice", "info", "debug"} {
		if value == name || value == strconv.Itoa(level) {
			return uint8(level), nil
		}
	}
	return 0, fmt.Errorf("无法确认 MaxLevelStore=%q，请使用原生等级名称或 0～7", value)
}

func readJournalSettings(source configSource, wanted map[string]string, values map[string]configValue) error {
	if !utf8.ValidString(source.data) || strings.ContainsRune(source.data, '\x00') {
		return fmt.Errorf("配置 %s 必须是有效 UTF-8 文本", source.path)
	}
	var section, continued string
	bomSeen := false
	scanner := bufio.NewScanner(strings.NewReader(source.data))
	for scanner.Scan() {
		physical := scanner.Text()
		leading := strings.TrimLeft(physical, " \t\r\n\v\f")
		if strings.HasPrefix(leading, "#") || strings.HasPrefix(leading, ";") {
			continue
		}
		if !bomSeen && strings.HasPrefix(physical, "\ufeff") {
			physical = strings.TrimPrefix(physical, "\ufeff")
			bomSeen = true
		}
		// 先按原始物理行连接，空行结束续行；尾部空白不能先裁剪。
		line := continued + physical
		backslashes := len(line) - len(strings.TrimRight(line, "\\"))
		if backslashes%2 != 0 {
			continued = strings.TrimSuffix(line, "\\") + " "
			continue
		}
		continued = ""
		if err := applyJournalLine(source.path, line, &section, wanted, values); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("读取配置 %s: %w", source.path, err)
	}
	if continued != "" {
		return applyJournalLine(source.path, continued, &section, wanted, values)
	}
	return nil
}

func applyJournalLine(path, line string, section *string, wanted map[string]string, values map[string]configValue) error {
	// systemd 只剥离 ASCII 空白，section 内的空格属于名称本身。
	line = strings.Trim(line, " \t\r\n\v\f")
	if strings.HasPrefix(line, "[") {
		if !strings.HasSuffix(line, "]") || len(line) <= 2 {
			return fmt.Errorf("配置 %s 的 section 语法不合法", path)
		}
		*section = line[1 : len(line)-1]
		return nil
	}
	if *section != "Journal" {
		return nil
	}
	key, value, found := strings.Cut(line, "=")
	key = strings.Trim(key, " \t\r\n\v\f")
	if found {
		value = strings.Trim(value, " \t\r\n\v\f")
		if key == "MaxLevelStore" {
			if _, err := storeLevel(value); err != nil {
				return fmt.Errorf("配置 %s: %w", path, err)
			}
		}
		if _, managed := wanted[key]; managed || key == "MaxLevelStore" {
			values[key] = configValue{value, path}
		}
	}
	return nil
}
