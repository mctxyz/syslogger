package hostinit

import (
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/mctxyz/syslogger/internal/catalog"
)

// TestCatalogRootOwnership 仅在专用 root 测试子进程中验证临时目录的数值身份。
func TestCatalogRootOwnership(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("跨 UID 创建由专用 root 测试子进程验证")
	}
	const uid, gid = 0, 12345
	root := filepath.Join(t.TempDir(), "catalog")
	init := testInitializer(io.Discard)
	if err := init.prepareCatalog(root, uid, gid); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := init.prepareCatalog(root, uid, gid); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	owner := info.Sys().(*syscall.Stat_t)
	if owner.Uid != uid || owner.Gid != gid || info.Mode()&(os.ModePerm|os.ModeSetgid|os.ModeSticky) != catalog.DirectoryMode || !os.SameFile(before, info) {
		t.Fatalf("根目录属主、权限或 inode 不符: uid=%d gid=%d mode=%v", owner.Uid, owner.Gid, info.Mode())
	}
	for _, identity := range [][2]int{{1000, gid}, {uid, 0}} {
		if err := os.Chown(root, identity[0], identity[1]); err != nil {
			t.Fatal(err)
		}
		if err := init.prepareCatalog(root, uid, gid); err != nil {
			t.Fatalf("已有根目录的身份错误未修复: %v", err)
		}
		info, err := os.Stat(root)
		if err != nil {
			t.Fatal(err)
		}
		owner := info.Sys().(*syscall.Stat_t)
		if int(owner.Uid) != uid || int(owner.Gid) != gid || info.Mode()&(os.ModePerm|os.ModeSetgid|os.ModeSticky) != catalog.DirectoryMode || !os.SameFile(before, info) {
			t.Fatalf("已有根目录未原地修复: uid=%d gid=%d mode=%v", owner.Uid, owner.Gid, info.Mode())
		}
	}
}

// TestSharedGroupCreation 在一次性容器内验证真实组创建，普通测试不会修改宿主组。
func TestSharedGroupCreation(t *testing.T) {
	if os.Geteuid() != 0 || os.Getenv("SYSLOGGER_GROUP_TEST_ISOLATED") != "1" {
		t.Skip("真实组创建仅在显式隔离的 root 测试环境运行")
	}
	init := testInitializer(io.Discard)
	gid, err := init.prepareGroup()
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile("/etc/group")
	if err != nil {
		t.Fatal(err)
	}
	again, err := init.prepareGroup()
	if err != nil || again != gid {
		t.Fatalf("已有组未复用: %d %d %v", gid, again, err)
	}
	after, err := os.ReadFile("/etc/group")
	if err != nil || string(before) != string(after) {
		t.Fatalf("复用组改变了成员配置: %v", err)
	}
	root := filepath.Join(t.TempDir(), "catalog")
	if err := init.prepareCatalog(root, 0, gid); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	owner := info.Sys().(*syscall.Stat_t)
	if owner.Uid != 0 || int(owner.Gid) != gid || info.Mode()&(os.ModePerm|os.ModeSetgid) != catalog.DirectoryMode {
		t.Fatalf("真实组根目录身份不符: uid=%d gid=%d mode=%v", owner.Uid, owner.Gid, info.Mode())
	}
}
