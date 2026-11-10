package hostinit

import (
	"context"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/mctxyz/syslogger/internal/catalog"
	"github.com/mctxyz/syslogger/internal/journalwire"
)

func TestInitRequiresRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("不在真实 root 环境运行主机初始化")
	}
	err := Run(context.Background(), Options{JournalMaxUse: [journalwire.GroupCount]string{"1G", "2G", "1G", "1G"}}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "需要 root") {
		t.Fatalf("普通账户应在任何主机变更前被拒绝: %v", err)
	}
}

func TestGroupLookupAndCreationFailure(t *testing.T) {
	group, lookupErr := user.LookupGroup(catalog.Group)
	directory := t.TempDir()
	logPath := filepath.Join(directory, "groupadd.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$INIT_TEST_GROUP_LOG\"\nexit 23\n"
	if err := os.WriteFile(filepath.Join(directory, "groupadd"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	t.Setenv("INIT_TEST_GROUP_LOG", logPath)
	gid, err := testInitializer(io.Discard).prepareGroup()
	if lookupErr != nil {
		if err == nil {
			t.Fatal("共享组创建失败时未报错")
		}
		data, readErr := os.ReadFile(logPath)
		if readErr != nil || strings.TrimSpace(string(data)) != "--system -- "+catalog.Group {
			t.Fatalf("组创建命令不符: %q %v", data, readErr)
		}
		return
	}
	if err != nil || strconv.Itoa(gid) != group.Gid {
		t.Fatalf("已有共享组解析错误: gid=%d err=%v", gid, err)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("已有组被重复创建: %v", err)
	}
}

func TestCatalogRepairPreservesContentsAndInodes(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, catalog.DirectoryMode); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(root, strings.Repeat("a", 64))
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(project, 0700); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(project, ".catalog.lock")
	dataPath := filepath.Join(project, strings.Repeat("b", 64)+".json")
	for _, path := range []string{lock, dataPath} {
		if err := os.WriteFile(path, []byte("保留"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	unknown := filepath.Join(project, "notes.txt")
	if err := os.WriteFile(unknown, []byte("无关文件"), 0600); err != nil {
		t.Fatal(err)
	}
	temporary := filepath.Join(root, ".catalog-project-unfinished")
	if err := os.Mkdir(temporary, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(temporary, 0700); err != nil {
		t.Fatal(err)
	}
	before := make(map[string]os.FileInfo)
	for _, path := range []string{root, project, lock, dataPath, unknown, temporary} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		before[path] = info
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := testInitializer(io.Discard).prepareCatalog(root, os.Geteuid(), os.Getegid()); err != nil {
			t.Fatal(err)
		}
	}
	for path, mode := range map[string]os.FileMode{root: catalog.DirectoryMode, project: catalog.DirectoryMode, lock: catalog.FileMode, dataPath: catalog.FileMode, unknown: 0600, temporary: 0700} {
		info, err := os.Stat(path)
		if err != nil || info.Mode()&(os.ModePerm|os.ModeSetgid) != mode {
			t.Fatalf("重复初始化权限修复结果不符: %s %v %v", path, info, err)
		}
		if !os.SameFile(before[path], info) {
			t.Fatalf("重复初始化替换了已有对象: %s", path)
		}
	}
	for _, path := range []string{lock, dataPath} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "保留" {
			t.Fatalf("重复初始化改变了数据: %s %q %v", path, data, err)
		}
	}
}

func TestCatalogRejectsUnsafeRoot(t *testing.T) {
	for _, kind := range []string{"symlink", "file", "acl", "default-acl"} {
		t.Run(kind, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "catalog")
			outside := t.TempDir()
			before, err := os.Stat(outside)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "symlink":
				if err := os.Symlink(outside, root); err != nil {
					t.Fatal(err)
				}
			case "file":
				if err := os.WriteFile(root, []byte("保留"), 0600); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.Mkdir(root, 0755); err != nil {
					t.Fatal(err)
				}
				name := "system.posix_acl_access"
				if kind == "default-acl" {
					name = "system.posix_acl_default"
				}
				setCatalogTestACL(t, root, name)
			}
			if err := testInitializer(io.Discard).prepareCatalog(root, os.Geteuid(), os.Getegid()); err == nil {
				t.Fatal("未拒绝不安全的名称根目录")
			} else if strings.Contains(kind, "acl") && !strings.Contains(err.Error(), "ACL") {
				t.Fatalf("未实际检查根目录 ACL: %v", err)
			}
			info, err := os.Stat(outside)
			if err != nil || info.Mode() != before.Mode() {
				t.Fatalf("改变了范围外对象权限: %v %v", info, err)
			}
		})
	}
}

func TestCatalogRepairsExistingPermissionDrift(t *testing.T) {
	for _, mode := range []os.FileMode{0700, 0775, os.ModeSetgid | 0755, os.ModeSticky | 0755} {
		root := t.TempDir()
		if err := os.Chmod(root, mode); err != nil {
			t.Fatal(err)
		}
		if err := testInitializer(io.Discard).prepareCatalog(root, os.Geteuid(), os.Getegid()); err != nil {
			t.Fatalf("未修复已有根目录权限: %v", err)
		}
		info, err := os.Stat(root)
		if err != nil || info.Mode()&(os.ModePerm|os.ModeSetgid|os.ModeSticky) != catalog.DirectoryMode {
			t.Fatalf("已有根目录权限未收敛到目标值: %v %v", info, err)
		}
	}
}

func TestJournalConfigOwnershipAndOverrides(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "journal.conf.d")
	path := filepath.Join(directory, configName)
	for _, capacity := range []string{"5G", "5G", "10G"} {
		if err := testInitializer(io.Discard).writeJournalConfig(path, journalConfig(capacity)); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(directory, configName))
		if err != nil || strings.Contains(string(data), "RuntimeMaxUse") {
			t.Fatalf("不应配置 /run 容量: %s %v", data, err)
		}
		sources := []configSource{{path, string(data)}}
		if err := checkJournalConfig(sources, journalSettings(capacity), journalwire.DebugGroup); err != nil {
			t.Fatal(err)
		}
		for _, override := range []string{"SystemMaxUse=1G", "Compress=no", "Storage=volatile", "ForwardToSyslog=yes"} {
			conflict := append(sources, configSource{filepath.Join(directory, "90-admin.conf"), "[Journal]\n" + override + "\n"})
			if err := checkJournalConfig(conflict, journalSettings(capacity), journalwire.DebugGroup); err == nil || !strings.Contains(err.Error(), "90-admin.conf") {
				t.Fatalf("管理员高优先级配置没有明确报错: %s %v", override, err)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(directory, configName), []byte("[Journal]\nCompress=no\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := testInitializer(io.Discard).writeJournalConfig(path, journalConfig("5G")); err == nil {
		t.Fatal("覆盖了同名非受管配置")
	}
	if !sameSetting("SystemMaxUse", "5120M", "5G") || !sameSetting("Compress", "true", "yes") {
		t.Fatal("等价设置被错误判定为冲突")
	}
}

func TestPrivateConfigDirectoryIsPreserved(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := testInitializer(io.Discard).writeJournalConfig(filepath.Join(directory, configName), journalConfig("5G")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(directory)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("初始化扩大了既有配置目录的读取权限: %v %v", info, err)
	}
}

func TestActivationRetriesAfterInterruptedApply(t *testing.T) {
	directory := t.TempDir()
	logPath := filepath.Join(directory, "commands")
	failPath := filepath.Join(directory, "fail-restart")
	if err := os.WriteFile(failPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$INIT_TEST_LOG\"\nif [ \"$1\" = restart ] && [ -f \"$INIT_TEST_FAIL\" ]; then exit 1; fi\nif [ \"$1\" = is-active ]; then exit 1; fi\n"
	if err := os.WriteFile(filepath.Join(directory, "systemctl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	t.Setenv("INIT_TEST_LOG", logPath)
	t.Setenv("INIT_TEST_FAIL", failPath)
	if err := testInitializer(io.Discard).activateJournal(journalwire.WarningGroup); err == nil {
		t.Fatal("未报告首次应用失败")
	}
	if err := os.Remove(failPath); err != nil {
		t.Fatal(err)
	}
	if err := testInitializer(io.Discard).activateJournal(journalwire.WarningGroup); err == nil {
		t.Fatal("未报告入口尚未就绪")
	}
	data, err := os.ReadFile(logPath)
	if err != nil || strings.Count(string(data), "restart systemd-journald@mct-syslogger-warning.service") != 2 {
		t.Fatalf("重复初始化没有重新应用配置: %s %v", data, err)
	}
	if strings.Contains(string(data), "restart systemd-journald.service") {
		t.Fatal("重启了默认 journal")
	}
}

func TestSystemdOfflineConfiguration(t *testing.T) {
	if _, err := exec.LookPath("systemd-analyze"); err != nil {
		t.Skip("本机没有离线配置合并工具")
	}
	root := t.TempDir()
	base := "systemd/" + journalBaseName(journalwire.DebugGroup)
	directory := filepath.Join(root, "etc", base+".d")
	if err := os.MkdirAll(filepath.Dir(directory), 0755); err != nil {
		t.Fatal(err)
	}
	if err := testInitializer(io.Discard).writeJournalConfig(filepath.Join(directory, configName), journalConfig("5G")); err != nil {
		t.Fatal(err)
	}
	merged, err := testInitializer(io.Discard).command("systemd-analyze", "--root="+root, "--no-pager", "cat-config", base)
	if err != nil {
		t.Fatal(err)
	}
	sources, err := journalConfigSources([]string{filepath.Join(root, "etc/systemd")}, 248, journalwire.DebugGroup)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkJournalConfig(sources, journalSettings("5G"), journalwire.DebugGroup); err != nil || !strings.Contains(merged, configName) {
		t.Fatalf("真实 systemd 离线配置合并不兼容: %v\n%s", err, merged)
	}
	conflict := filepath.Join(directory, "90-admin.conf")
	if err := os.WriteFile(conflict, []byte("[Journal]\nSystemMaxUse=1G\n"), 0644); err != nil {
		t.Fatal(err)
	}
	merged, err = testInitializer(io.Discard).command("systemd-analyze", "--root="+root, "--no-pager", "cat-config", base)
	if err != nil {
		t.Fatal(err)
	}
	sources, err = journalConfigSources([]string{filepath.Join(root, "etc/systemd")}, 248, journalwire.DebugGroup)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkJournalConfig(sources, journalSettings("5G"), journalwire.DebugGroup); err == nil || !strings.Contains(err.Error(), "90-admin.conf") || !strings.Contains(merged, "90-admin.conf") {
		t.Fatalf("真实 systemd 优先级覆盖未报告: %v", err)
	}
}
