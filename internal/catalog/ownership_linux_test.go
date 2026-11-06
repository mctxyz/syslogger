package catalog

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestOwnershipAcrossRootAndApp 仅在独立 root 测试进程中运行，数值身份只用于临时目录。
func TestOwnershipAcrossRootAndApp(t *testing.T) {
	const workerVariable = "SYSLOGGER_OWNER_TEST_DIRECTORY"
	const ownerUID, ownerGID = 1000, 1000
	at := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	delta := activeFixture(Instance{Project: "project", InstanceID: "instance", Components: []Component{{Name: "rpc"}}}, at)
	if directory := os.Getenv(workerVariable); directory != "" {
		// 先由 root 进入专属目录，再仅在该测试子进程中丢弃附加组并切换身份。
		if err := os.Chdir(directory); err != nil {
			t.Fatal(err)
		}
		uid, err := strconv.Atoi(os.Getenv("SYSLOGGER_OWNER_TEST_UID"))
		if err != nil {
			t.Fatal(err)
		}
		gid := 0
		if uid != 0 {
			gid = ownerGID
		}
		if err := unix.Setgroups(nil); err != nil {
			t.Fatal(err)
		}
		if err := unix.Setgid(gid); err != nil {
			t.Fatal(err)
		}
		if err := unix.Setuid(uid); err != nil {
			t.Fatal(err)
		}
		groups, err := os.Getgroups()
		if err != nil || len(groups) != 0 || os.Geteuid() != uid || os.Getegid() != gid {
			t.Fatalf("测试子进程身份不符: uid=%d gid=%d groups=%v err=%v", os.Geteuid(), os.Getegid(), groups, err)
		}
		count, err := strconv.Atoi(os.Getenv("SYSLOGGER_OWNER_TEST_COUNT"))
		if err != nil {
			t.Fatal(err)
		}
		store := Store{Root: "catalog"}
		for index := 0; index < count; index++ {
			if err := store.Merge(delta, at); err != nil {
				t.Fatal(err)
			}
		}
		assertCatalogOwner(t, store, ownerUID, ownerGID)
		return
	}
	if os.Geteuid() != 0 {
		t.Skip("跨 UID 权限场景由专用 root 测试子进程验证")
	}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0755); err != nil {
		t.Fatal(err)
	}
	store := Store{Root: filepath.Join(directory, "catalog")}
	if err := os.Mkdir(store.Root, DirectoryMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(store.Root, ownerUID, ownerGID); err != nil {
		t.Fatal(err)
	}
	if err := store.Merge(delta, at); err != nil {
		t.Fatal(err)
	}
	assertCatalogOwner(t, store, ownerUID, ownerGID)
	lockPath := filepath.Join(filepath.Dir(store.Path("project", "instance")), LockName)
	before, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	worker := func(uid, count int) *exec.Cmd {
		command := exec.Command(os.Args[0], "-test.run=^TestOwnershipAcrossRootAndApp$")
		command.Env = append(os.Environ(),
			workerVariable+"="+directory,
			"SYSLOGGER_OWNER_TEST_UID="+strconv.Itoa(uid),
			"SYSLOGGER_OWNER_TEST_COUNT="+strconv.Itoa(count),
		)
		return command
	}
	for index := 0; index < 2; index++ {
		if output, err := worker(ownerUID, 1).CombinedOutput(); err != nil {
			t.Fatalf("app 更新失败: %s %v", output, err)
		}
		if index == 0 {
			if err := store.Merge(delta, at); err != nil {
				t.Fatal(err)
			}
			assertCatalogOwner(t, store, ownerUID, ownerGID)
		}
	}
	// 构造过期组件，覆盖 Prune 通过临时文件重写清单时的所有者。
	instances, err := store.Read()
	if err != nil || len(instances) != 1 {
		t.Fatalf("读取跨身份清单失败: %v %v", instances, err)
	}
	old := at.AddDate(-1, 0, -1)
	instances[0].Components = append(instances[0].Components, Component{Name: "expired", CreatedAt: old, LastActiveAt: old})
	data, err := json.Marshal(instances[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Path("project", "instance"), data, FileMode); err != nil {
		t.Fatal(err)
	}
	if err := store.Prune(at); err != nil {
		t.Fatal(err)
	}
	assertCatalogOwner(t, store, ownerUID, ownerGID)
	var workers sync.WaitGroup
	for _, uid := range []int{0, ownerUID, 0, ownerUID} {
		workers.Add(1)
		go func(uid int) {
			defer workers.Done()
			if output, err := worker(uid, 25).CombinedOutput(); err != nil {
				t.Errorf("并发更新失败: uid=%d output=%s err=%v", uid, output, err)
			}
		}(uid)
	}
	workers.Wait()
	instances, err = store.Read()
	if err != nil || len(instances) != 1 || len(instances[0].Components) != 1 || componentCount(instances[0].Components[0]) != 104 {
		t.Fatalf("跨身份发布或清理丢失记录: %+v %v", instances, err)
	}
	assertCatalogOwner(t, store, ownerUID, ownerGID)
	after, err := os.Stat(lockPath)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("跨身份写入替换了锁 inode: %v", err)
	}
}

func assertCatalogOwner(t *testing.T, store Store, uid, gid int) {
	t.Helper()
	path := store.Path("project", "instance")
	for path, mode := range map[string]os.FileMode{
		store.Root:         DirectoryMode,
		filepath.Dir(path): DirectoryMode,
		path:               FileMode,
		filepath.Join(filepath.Dir(path), LockName): FileMode,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		owner := info.Sys().(*syscall.Stat_t)
		if int(owner.Uid) != uid || int(owner.Gid) != gid || info.Mode()&(os.ModePerm|os.ModeSetgid|os.ModeSticky) != mode {
			t.Fatalf("属主或权限不符: %s uid=%d gid=%d mode=%v", path, owner.Uid, owner.Gid, info.Mode())
		}
	}
}
