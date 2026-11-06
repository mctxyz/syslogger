package hostinit

import (
	"context"
	"encoding/binary"
	"errors"
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
	"golang.org/x/sys/unix"
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

func TestFixedAccountLookup(t *testing.T) {
	account, lookupErr := user.Lookup(catalog.User)
	uid, gid, err := testInitializer(io.Discard).prepareAccount()
	if lookupErr != nil {
		if err == nil {
			t.Fatal("固定账户不存在时未报错")
		}
		return
	}
	if err != nil || strconv.Itoa(uid) != account.Uid || strconv.Itoa(gid) != account.Gid {
		t.Fatalf("固定账户身份解析错误: uid=%d gid=%d err=%v", uid, gid, err)
	}
}

func TestCatalogMigrationPreservesDataAndLock(t *testing.T) {
	root := filepath.Join(t.TempDir(), "catalog")
	project := filepath.Join(root, strings.Repeat("a", 64))
	if err := os.MkdirAll(project, 0777); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(project, ".catalog.lock")
	dataPath := filepath.Join(project, strings.Repeat("b", 64)+".json")
	for _, path := range []string{lock, dataPath} {
		if err := os.WriteFile(path, []byte("保留"), 0666); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.Stat(lock)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := testInitializer(io.Discard).prepareCatalog(root, os.Geteuid(), os.Getegid()); err != nil {
			t.Fatal(err)
		}
	}
	for path, mode := range map[string]os.FileMode{root: catalog.DirectoryMode, project: catalog.DirectoryMode, lock: catalog.FileMode, dataPath: catalog.FileMode} {
		info, err := os.Stat(path)
		if err != nil || info.Mode()&(os.ModePerm|os.ModeSetgid) != mode {
			t.Fatalf("迁移权限错误: %s %v %v", path, info, err)
		}
	}
	after, err := os.Stat(lock)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("迁移替换了稳定锁 inode: %v", err)
	}
	data, err := os.ReadFile(dataPath)
	if err != nil || string(data) != "保留" {
		t.Fatalf("迁移改变了数据: %q %v", data, err)
	}
}

func TestMigrationRejectsLinksAndACL(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "acl", "default-acl"} {
		t.Run(kind, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "catalog")
			project := filepath.Join(root, strings.Repeat("a", 64))
			if err := os.MkdirAll(project, 0755); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(outside, []byte("范围外"), 0600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(project, ".catalog.lock")
			switch kind {
			case "symlink":
				if err := os.Symlink(outside, path); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(outside, path); err != nil {
					t.Fatal(err)
				}
			default:
				name := "system.posix_acl_access"
				if kind == "default-acl" {
					name = "system.posix_acl_default"
				}
				acl := make([]byte, 4+5*8)
				binary.LittleEndian.PutUint32(acl, 2)
				for index, entry := range []struct {
					tag  uint16
					perm uint16
					id   uint32
				}{{1, 7, ^uint32(0)}, {2, 6, uint32(os.Getuid() + 1)}, {4, 5, ^uint32(0)}, {16, 7, ^uint32(0)}, {32, 5, ^uint32(0)}} {
					offset := 4 + index*8
					binary.LittleEndian.PutUint16(acl[offset:], entry.tag)
					binary.LittleEndian.PutUint16(acl[offset+2:], entry.perm)
					binary.LittleEndian.PutUint32(acl[offset+4:], entry.id)
				}
				if err := unix.Setxattr(project, name, acl, 0); errors.Is(err, unix.ENOTSUP) {
					t.Skip("测试文件系统不支持 POSIX ACL")
				} else if err != nil {
					t.Fatal(err)
				}
			}
			if err := testInitializer(io.Discard).prepareCatalog(root, os.Geteuid(), os.Getegid()); err == nil {
				t.Fatal("未拒绝越界链接或 ACL")
			}
			info, err := os.Stat(outside)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("改变了范围外对象权限: %v %v", info, err)
			}
		})
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
