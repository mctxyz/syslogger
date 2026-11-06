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
	const uid, gid = 1000, 1000
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
	for _, identity := range [][2]int{{0, gid}, {uid, 0}} {
		if err := os.Chown(root, identity[0], identity[1]); err != nil {
			t.Fatal(err)
		}
		if err := init.prepareCatalog(root, uid, gid); err == nil {
			t.Fatal("已有根目录的身份错误被自动修复")
		}
		info, err := os.Stat(root)
		if err != nil {
			t.Fatal(err)
		}
		owner := info.Sys().(*syscall.Stat_t)
		if int(owner.Uid) != identity[0] || int(owner.Gid) != identity[1] {
			t.Fatalf("改写了已有根目录的身份: uid=%d gid=%d", owner.Uid, owner.Gid)
		}
	}
}
