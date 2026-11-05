package hostinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		if err := checkJournalConfig(sources, journalSettings("5G")); err == nil || !strings.Contains(err.Error(), "90-admin.conf") {
			t.Fatalf("正文注释、BOM 或损坏 section 绕过了检查: %q %v", text, err)
		}
	}
	for _, text := range []string{"# /a/path\n", "[Other]\nCompress=no\n", "[Journal]\nSystemMaxUse=\\\n# 续行注释\n5G\n", "[Journal]\nCompress=yes\\"} {
		sources := []configSource{{"60-owned.conf", journalConfig("5G")}, {"90-admin.conf", text}}
		if err := checkJournalConfig(sources, journalSettings("5G")); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCleanupRevealsLowerPriorityMain(t *testing.T) {
	root := t.TempDir()
	etc := filepath.Join(root, "etc")
	run := filepath.Join(root, "run")
	if err := os.MkdirAll(run, 0755); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(etc, journalBaseName)
	if err := writeJournalConfig(main, journalConfig("5G")); err != nil {
		t.Fatal(err)
	}
	if err := writeJournalConfig(filepath.Join(main+".d", configName), journalConfig("5G")); err != nil {
		t.Fatal(err)
	}
	exposed := filepath.Join(run, journalBaseName)
	if err := os.WriteFile(exposed, []byte("[broken\n"), 0644); err != nil {
		t.Fatal(err)
	}
	sources, err := journalConfigSources([]string{etc, run}, 256)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkJournalConfig(sources, journalSettings("5G")); err != nil {
		t.Fatal(err)
	}
	if err := removeManagedConfig(main); err != nil {
		t.Fatal(err)
	}
	sources, err = journalConfigSources([]string{etc, run}, 256)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkJournalConfig(sources, journalSettings("5G")); err == nil || !strings.Contains(err.Error(), exposed) {
		t.Fatalf("清理后遗漏了新显露的主文件: %v", err)
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
	base := journalBaseName
	main := write(0, base, journalConfig("5G"))
	write(0, filepath.Join(base+".d", configName), journalConfig("5G"))
	legacy := write(4, filepath.Join(base+".d", "90-admin.conf"), "[Journal]\nSystemMaxUse=1G\n")
	for _, version := range []int{245, 247} {
		sources, err := journalConfigSources(directories, version)
		if err != nil || len(sources) != 1 || sources[0].path != main {
			t.Fatalf("旧版命名空间读取了 drop-in: %d %v %v", version, sources, err)
		}
	}
	for _, version := range []int{248, 255, 256, 257} {
		sources, err := journalConfigSources(directories, version)
		if err != nil {
			t.Fatal(err)
		}
		if err := checkJournalConfig(sources, journalSettings("5G")); err == nil || !strings.Contains(err.Error(), legacy) {
			t.Fatalf("遗漏了 split-usr 的 /lib 配置: %d %v", version, err)
		}
	}
	masked := filepath.Join(directories[0], base+".d", "90-admin.conf")
	if err := os.Symlink("/dev/null", masked); err != nil {
		t.Fatal(err)
	}
	sources, err := journalConfigSources(directories, 257)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkJournalConfig(sources, journalSettings("5G")); err != nil {
		t.Fatalf("同名 /dev/null 没有屏蔽低优先级配置: %v", err)
	}
	if err := os.Remove(main); err != nil {
		t.Fatal(err)
	}
	write(1, base, journalConfig("5G"))
	oldSources, err := journalConfigSources(directories, 255)
	if err != nil {
		t.Fatal(err)
	}
	newSources, err := journalConfigSources(directories, 256)
	if err != nil {
		t.Fatal(err)
	}
	if len(newSources) != len(oldSources)+1 || newSources[0].path != filepath.Join(directories[1], base) {
		t.Fatal("256 主文件目录搜索不符合版本契约")
	}
}

func TestManagedConfigVersionMigration(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "systemd")
	main := filepath.Join(directory, journalBaseName)
	dropin := filepath.Join(main+".d", configName)
	if err := writeJournalConfig(main, journalConfig("5G")); err != nil {
		t.Fatal(err)
	}
	if err := writeJournalConfig(dropin, journalConfig("10G")); err != nil {
		t.Fatal(err)
	}
	if err := removeManagedConfig(main); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(main); !os.IsNotExist(err) {
		t.Fatalf("升级后旧受管主文件未清理: %v", err)
	}
	if err := os.WriteFile(main, []byte("[Journal]\nCompress=no\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := removeManagedConfig(main); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(main)
	if err != nil || !strings.Contains(string(data), "Compress=no") {
		t.Fatalf("升级清理删除了管理员配置: %s %v", data, err)
	}
	if err := removeManagedConfig(dropin); err != nil {
		t.Fatal(err)
	}
	if err := removeManagedConfig(dropin); err != nil {
		t.Fatalf("中断后的重复清理不能恢复: %v", err)
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
