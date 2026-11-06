package hostinit

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/mctxyz/syslogger/internal/journalwire"
)

func TestConfigCommentsAndSyntax(t *testing.T) {
	for _, text := range []string{
		"[Journal]\n# /var/log/journal 的部署说明\nSystemMaxUse=1G\n",
		"[Journal]\n# /etc/systemd/journald.conf\nCompress=no\n",
		"\ufeff[Journal]\nStorage=volatile\n",
		"[Journal]\nForwardToSyslog=yes\n[broken\nForwardToSyslog=no\n",
		"[Journal]\nCompress=no\n[ Journal ]\nCompress=yes\n",
		"[Journal]\nCompress=\u2003yes\n",
		"[Journal]\nCompress=no\x00\nCompress=yes\n",
		"[Journal]\nSystemMaxUse=1G\nSystemMaxUse=\\\n\n5G\n",
		"[Journal]\nMaxLevelStore=info\\\n\nForwardToSyslog=yes\n",
		"[Journal]\nCompress=no\nCompress=\\ \nyes\n",
		"\n\ufeff[Journal]\nCompress=no\n",
	} {
		sources := []configSource{{"60-owned.conf", journalConfig("5G")}, {"90-admin.conf", text}}
		if err := checkJournalConfig(sources, journalSettings("5G"), journalwire.DebugGroup); err == nil || !strings.Contains(err.Error(), "90-admin.conf") {
			t.Fatalf("正文注释、BOM 或损坏 section 绕过了检查: %q %v", text, err)
		}
	}
	for _, text := range []string{"# /a/path\n", "[Other]\nCompress=no\n", "[Journal]\nSystemMaxUse=\\\n# 续行注释\n5G\n", "[Journal]\nCompress=yes\\"} {
		sources := []configSource{{"60-owned.conf", journalConfig("5G")}, {"90-admin.conf", text}}
		if err := checkJournalConfig(sources, journalSettings("5G"), journalwire.DebugGroup); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConfigSourceVersionsAndPrecedence(t *testing.T) {
	root := t.TempDir()
	var directories []string
	for _, name := range []string{"etc", "run", "usr-local", "usr-lib", "lib"} {
		directories = append(directories, filepath.Join(root, name))
	}
	write := func(index int, name, content string) string {
		t.Helper()
		path := filepath.Join(directories[index], name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	base := journalBaseName(journalwire.DebugGroup)
	main := write(0, base, journalConfig("5G"))
	write(0, filepath.Join(base+".d", configName), journalConfig("5G"))
	legacy := write(4, filepath.Join(base+".d", "90-admin.conf"), "[Journal]\nSystemMaxUse=1G\n")
	for _, version := range []int{245, 247} {
		sources, err := journalConfigSources(directories, version, journalwire.DebugGroup)
		if err != nil || len(sources) != 1 || sources[0].path != main {
			t.Fatalf("旧版命名空间读取了 drop-in: %d %v %v", version, sources, err)
		}
	}
	for _, version := range []int{248, 255, 256, 257} {
		sources, err := journalConfigSources(directories, version, journalwire.DebugGroup)
		if err != nil {
			t.Fatal(err)
		}
		if err := checkJournalConfig(sources, journalSettings("5G"), journalwire.DebugGroup); err == nil || !strings.Contains(err.Error(), legacy) {
			t.Fatalf("遗漏了 split-usr 的 /lib 配置: %d %v", version, err)
		}
	}
	masked := filepath.Join(directories[0], base+".d", "90-admin.conf")
	if err := os.Symlink("/dev/null", masked); err != nil {
		t.Fatal(err)
	}
	sources, err := journalConfigSources(directories, 257, journalwire.DebugGroup)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkJournalConfig(sources, journalSettings("5G"), journalwire.DebugGroup); err != nil {
		t.Fatalf("同名 /dev/null 没有屏蔽低优先级配置: %v", err)
	}
	if err := os.Remove(main); err != nil {
		t.Fatal(err)
	}
	write(1, base, journalConfig("5G"))
	oldSources, err := journalConfigSources(directories, 255, journalwire.DebugGroup)
	if err != nil {
		t.Fatal(err)
	}
	newSources, err := journalConfigSources(directories, 256, journalwire.DebugGroup)
	if err != nil {
		t.Fatal(err)
	}
	if len(newSources) != len(oldSources)+1 || newSources[0].path != filepath.Join(directories[1], base) {
		t.Fatal("256 主文件目录搜索不符合版本契约")
	}
}

func TestInstalledVersionParsing(t *testing.T) {
	for input, expected := range map[string]int{"systemd 245 (245.4)\n+PAM": 245, "systemd 257 (257.13)": 257, "systemd 260~rc1": 260} {
		got, err := parseSystemdVersion(input)
		if err != nil || got != expected {
			t.Fatalf("版本解析错误: %q %d %v", input, got, err)
		}
	}
	for _, input := range []string{"", "systemd unknown", "systemd 244"} {
		if _, err := parseSystemdVersion(input); err == nil {
			t.Fatalf("接受了不支持的版本: %q", input)
		}
	}
}

func TestStoreLevelRespectsEachGroup(t *testing.T) {
	for group := journalwire.DebugGroup; group < journalwire.GroupCount; group++ {
		for level := 0; level < 8; level++ {
			text := "[Journal]\nMaxLevelStore=" + strconv.Itoa(level) + "\n"
			sources := []configSource{{"60-owned.conf", journalConfig("1G")}, {"90-admin.conf", text}}
			err := checkJournalConfig(sources, journalSettings("1G"), group)
			if (err == nil) != (uint8(level) >= group.MaxSeverity()) {
				t.Fatalf("group=%s level=%d 检查错误: %v", group.Name(), level, err)
			}
			if err != nil && !strings.Contains(err.Error(), "90-admin.conf") {
				t.Fatalf("冲突没有来源: %v", err)
			}
		}
	}
	for _, text := range []string{
		"[Journal]\nMaxLevelStore=\n",
		"[Journal]\nMaxLevelStore=info\nMaxLevelStore=\n",
		"[Journal]\nMaxLevelStore=\nMaxLevelStore=debug\n",
		"[Journal]\nMaxLevelStore=unknown\n",
		"[Journal]\nMaxLevelStore=8\n",
	} {
		sources := []configSource{{"60-owned.conf", journalConfig("1G")}, {"90-admin.conf", text}}
		if err := checkJournalConfig(sources, journalSettings("1G"), journalwire.DebugGroup); err == nil {
			t.Fatalf("未拒绝无法确认的等级赋值: %q", text)
		}
	}
	sources := []configSource{{"60-owned.conf", journalConfig("1G")}, {"90-admin.conf", "[Journal]\nMaxLevelStore=info\nMaxLevelStore=debug\n"}}
	if err := checkJournalConfig(sources, journalSettings("1G"), journalwire.DebugGroup); err != nil {
		t.Fatalf("有效的最终赋值未覆盖前值: %v", err)
	}
}

func TestGroupConfigSourcesAreIndependent(t *testing.T) {
	directory := t.TempDir()
	for group := journalwire.DebugGroup; group < journalwire.GroupCount; group++ {
		path := filepath.Join(directory, journalBaseName(group)+".d", configName)
		if err := testInitializer(io.Discard).writeJournalConfig(path, journalConfig(DefaultJournalMaxUse(group))); err != nil {
			t.Fatal(err)
		}
	}
	for group := journalwire.DebugGroup; group < journalwire.GroupCount; group++ {
		sources, err := journalConfigSources([]string{directory}, 256, group)
		if err != nil || len(sources) != 1 || !strings.Contains(sources[0].path, group.Namespace()) {
			t.Fatalf("%s 读取了其他空间的配置: %v %v", group.Name(), sources, err)
		}
		if err := checkJournalConfig(sources, journalSettings(DefaultJournalMaxUse(group)), group); err != nil {
			t.Fatal(err)
		}
	}
}
