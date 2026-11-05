package catalog

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestPermissionsUnderRestrictiveUmask(t *testing.T) {
	const variable = "SYSLOGGER_TEST_CATALOG_UMASK_ROOT"
	if root := os.Getenv(variable); root != "" {
		// umask 属于进程状态，只在专用子进程中改变以保护其他并发测试。
		unix.Umask(077)
		store := Store{Root: root}
		at := time.Now().UTC()
		instance := activeFixture(Instance{Project: "project", InstanceID: "instance", Components: []Component{{Name: "rpc"}}}, at)
		for count := 0; count < 2; count++ {
			if err := store.Merge(instance, at); err != nil {
				t.Fatal(err)
			}
		}
		path := store.Path(instance.Project, instance.InstanceID)
		for name, mode := range map[string]os.FileMode{
			filepath.Dir(path): DirectoryMode,
			path:               FileMode,
			filepath.Join(filepath.Dir(path), ".catalog.lock"): FileMode,
		} {
			info, err := os.Stat(name)
			if err != nil || info.Mode()&(os.ModeSetgid|os.ModePerm) != mode {
				t.Fatalf("umask 破坏共享权限: %s %v %v", name, info, err)
			}
		}
		return
	}
	root := t.TempDir()
	if err := os.Chmod(root, DirectoryMode); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestPermissionsUnderRestrictiveUmask$")
	command.Env = append(os.Environ(), variable+"="+root)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("隔离 umask 验证失败: %s %v", output, err)
	}
}
