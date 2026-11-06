package hostinit

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/mctxyz/syslogger/internal/catalog"
)

// TestCatalogMigrationToApp 将特权修改限制在临时目录，数值身份模拟已有 app 账户。
func TestCatalogMigrationToApp(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("跨 UID 迁移由专用 root 测试子进程验证")
	}
	const uid, gid = 1000, 1000
	root := filepath.Join(t.TempDir(), "catalog")
	project := filepath.Join(root, strings.Repeat("a", 64))
	if err := os.MkdirAll(project, 0755); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{root, project} {
		if err := os.Chmod(directory, os.ModeSetgid|0775); err != nil {
			t.Fatal(err)
		}
	}
	lock := filepath.Join(project, catalog.LockName)
	dataPath := filepath.Join(project, strings.Repeat("b", 64)+".json")
	for _, path := range []string{lock, dataPath} {
		if err := os.WriteFile(path, []byte("保留内容"), 0664); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.Stat(lock)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := testInitializer(io.Discard).prepareCatalog(root, uid, gid); err != nil {
			t.Fatal(err)
		}
	}
	for path, mode := range map[string]os.FileMode{root: catalog.DirectoryMode, project: catalog.DirectoryMode, lock: catalog.FileMode, dataPath: catalog.FileMode} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		owner := info.Sys().(*syscall.Stat_t)
		if owner.Uid != uid || owner.Gid != gid || info.Mode()&(os.ModePerm|os.ModeSetgid|os.ModeSticky) != mode {
			t.Fatalf("迁移后的属主或权限错误: %s uid=%d gid=%d mode=%v", path, owner.Uid, owner.Gid, info.Mode())
		}
	}
	after, err := os.Stat(lock)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("迁移替换了锁 inode: %v", err)
	}
	data, err := os.ReadFile(dataPath)
	if err != nil || string(data) != "保留内容" {
		t.Fatalf("迁移改变了数据: %q %v", data, err)
	}
}
