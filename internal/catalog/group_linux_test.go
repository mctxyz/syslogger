package catalog

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const sharedTestGID = 12345
const groupWorkerDirectory = "SYSLOGGER_GROUP_TEST_DIRECTORY"

type groupWorker struct {
	uid   int
	count int
	mode  string
}

// TestGroupSharingAcrossUsers 仅在独立 root 测试环境中切换子进程身份，所有数据位于临时目录。
func TestGroupSharingAcrossUsers(t *testing.T) {
	at := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	if directory := os.Getenv(groupWorkerDirectory); directory != "" {
		runGroupWorker(t, directory, at)
		return
	}
	if os.Geteuid() != 0 {
		t.Skip("跨 UID 与附加组场景由独立 root 测试环境验证")
	}
	for _, scenario := range []struct {
		name      string
		first     []groupWorker
		empty     bool
		rootOwner bool
	}{
		{name: "user-first", first: []groupWorker{{1001, 1, "write"}}},
		{name: "root-first", first: []groupWorker{{0, 1, "write"}}},
		{name: "root-owned-after-creation", first: []groupWorker{{1001, 1, "write"}}, rootOwner: true},
		{name: "concurrent-first-create", first: []groupWorker{{1001, 1, "write"}, {1002, 1, "write"}, {1001, 1, "write"}, {1002, 1, "write"}}},
		{name: "existing-empty-root-project", first: []groupWorker{{1001, 1, "write"}, {1002, 1, "write"}}, empty: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			directory, store := preparedGroupStore(t)
			path := store.Path("project", "instance")
			project := filepath.Dir(path)
			if scenario.empty {
				if err := os.Mkdir(project, DirectoryMode); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(project, DirectoryMode); err != nil {
					t.Fatal(err)
				}
			}
			runGroupWorkers(t, directory, scenario.first)
			if scenario.rootOwner {
				for name, mode := range map[string]os.FileMode{
					project:                          DirectoryMode,
					path:                             FileMode,
					filepath.Join(project, LockName): FileMode,
				} {
					if err := os.Chown(name, 0, -1); err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(name, mode); err != nil {
						t.Fatal(err)
					}
				}
			}
			beforeProject, err := os.Stat(project)
			if err != nil {
				t.Fatal(err)
			}
			owner := beforeProject.Sys().(*syscall.Stat_t).Uid
			if scenario.rootOwner || scenario.empty {
				if owner != 0 {
					t.Fatalf("已有 root 项目目录被换属主: %d", owner)
				}
			} else if len(scenario.first) == 1 && owner != uint32(scenario.first[0].uid) {
				t.Fatalf("新项目未保留创建者 UID: %d", owner)
			}
			lockPath := filepath.Join(project, LockName)
			beforeLock, err := os.Stat(lockPath)
			if err != nil {
				t.Fatal(err)
			}
			// 通过真实合并加入过期项，再由另一普通账户重写清理 root/其他用户的文件。
			old := at.AddDate(-1, 0, -1)
			expired := activeFixture(Instance{Project: "project", InstanceID: "instance", Components: []Component{{Name: "expired"}}}, old)
			if err := store.Merge(expired, old); err != nil {
				t.Fatal(err)
			}
			runGroupWorkers(t, directory, []groupWorker{{1002, 0, "prune"}})
			runGroupWorkers(t, directory, []groupWorker{{0, 25, "write"}, {1001, 25, "write"}, {1002, 25, "write"}, {1002, 0, "prune"}})
			runGroupWorkers(t, directory, []groupWorker{{1003, 0, "read-only"}})
			instances, err := store.Read()
			if err != nil || len(instances) != 1 || len(instances[0].Components) != 1 {
				t.Fatalf("跨用户合并或清理结果不符: %+v %v", instances, err)
			}
			want := uint64(75)
			for _, worker := range scenario.first {
				want += uint64(worker.count)
			}
			if instances[0].Components[0].Name != "rpc" || componentCount(instances[0].Components[0]) != want {
				t.Fatalf("跨用户并发丢失增量或组外账户写入成功: %+v，期望 %d", instances, want)
			}
			assertCatalogGroup(t, store)
			afterProject, err := os.Stat(project)
			if err != nil || !os.SameFile(beforeProject, afterProject) || afterProject.Sys().(*syscall.Stat_t).Uid != owner {
				t.Fatalf("共用项目时替换了目录或抢占属主: %v", err)
			}
			afterLock, err := os.Stat(lockPath)
			if err != nil || !os.SameFile(beforeLock, afterLock) || afterLock.Sys().(*syscall.Stat_t).Uid != beforeLock.Sys().(*syscall.Stat_t).Uid {
				t.Fatalf("跨用户更新替换了稳定锁或抢占属主: %v", err)
			}
			for name, count := range map[string]int{store.Root: 1, project: 2} {
				entries, err := os.ReadDir(name)
				if err != nil || len(entries) != count {
					t.Fatalf("成功发布后遗留临时对象: %s %+v %v", name, entries, err)
				}
			}
		})
	}
}

func preparedGroupStore(t *testing.T) (string, Store) {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0755); err != nil {
		t.Fatal(err)
	}
	store := Store{Root: filepath.Join(directory, "catalog")}
	if err := os.Mkdir(store.Root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(store.Root, 0, sharedTestGID); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store.Root, DirectoryMode); err != nil {
		t.Fatal(err)
	}
	return directory, store
}

// runGroupWorkers 使用管道屏障同步首次操作，确保严格 umask 与跨 UID 竞争组合覆盖。
func runGroupWorkers(t *testing.T, directory string, workers []groupWorker) {
	t.Helper()
	ready, notify, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer ready.Close()
	defer notify.Close()
	start, release, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer start.Close()
	defer release.Close()
	var commands []*exec.Cmd
	var outputs []*bytes.Buffer
	for _, worker := range workers {
		command := exec.Command(os.Args[0], "-test.run=^TestGroupSharingAcrossUsers$", "-test.timeout=30s")
		command.Env = append(os.Environ(),
			groupWorkerDirectory+"="+directory,
			"SYSLOGGER_GROUP_TEST_UID="+strconv.Itoa(worker.uid),
			"SYSLOGGER_GROUP_TEST_COUNT="+strconv.Itoa(worker.count),
			"SYSLOGGER_GROUP_TEST_MODE="+worker.mode,
		)
		command.ExtraFiles = []*os.File{notify, start}
		output := &bytes.Buffer{}
		command.Stdout = output
		command.Stderr = output
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if command.ProcessState == nil {
				command.Process.Kill()
				command.Wait()
			}
		})
		commands = append(commands, command)
		outputs = append(outputs, output)
	}
	if err := ready.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(ready, make([]byte, len(workers))); err != nil {
		t.Fatalf("子进程未到达组权限测试屏障: %v", err)
	}
	if err := release.Close(); err != nil {
		t.Fatal(err)
	}
	for index, command := range commands {
		if err := command.Wait(); err != nil {
			t.Errorf("跨用户操作失败: uid=%d mode=%s %s %v", workers[index].uid, workers[index].mode, outputs[index], err)
		}
	}
}

func runGroupWorker(t *testing.T, directory string, at time.Time) {
	t.Helper()
	if err := os.Chdir(directory); err != nil {
		t.Fatal(err)
	}
	uid, err := strconv.Atoi(os.Getenv("SYSLOGGER_GROUP_TEST_UID"))
	if err != nil {
		t.Fatal(err)
	}
	count, err := strconv.Atoi(os.Getenv("SYSLOGGER_GROUP_TEST_COUNT"))
	if err != nil {
		t.Fatal(err)
	}
	mode := os.Getenv("SYSLOGGER_GROUP_TEST_MODE")
	gid := 0
	var groups []int
	if uid != 0 {
		gid = 20000 + uid
		if mode != "read-only" {
			groups = []int{sharedTestGID}
		}
	}
	if err := unix.Setgroups(groups); err != nil {
		t.Fatal(err)
	}
	if err := unix.Setgid(gid); err != nil {
		t.Fatal(err)
	}
	if err := unix.Setuid(uid); err != nil {
		t.Fatal(err)
	}
	actualGroups, err := os.Getgroups()
	if err != nil || os.Geteuid() != uid || os.Getegid() != gid || len(actualGroups) != len(groups) {
		t.Fatalf("测试进程身份不符: uid=%d gid=%d groups=%v %v", os.Geteuid(), os.Getegid(), actualGroups, err)
	}
	if len(groups) == 1 && actualGroups[0] != sharedTestGID {
		t.Fatalf("未获得测试附加组: %v", actualGroups)
	}
	unix.Umask(077)
	notify := os.NewFile(3, "ready")
	defer notify.Close()
	start := os.NewFile(4, "start")
	defer start.Close()
	if _, err := notify.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	if _, err := start.Read(make([]byte, 1)); err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	store := Store{Root: "catalog"}
	delta := activeFixture(Instance{Project: "project", InstanceID: "instance", Components: []Component{{Name: "rpc"}}}, at)
	switch mode {
	case "prune":
		if err := store.Prune(at); err != nil {
			t.Fatal(err)
		}
	case "read-only":
		instances, err := store.Read()
		if err != nil || len(instances) != 1 {
			t.Fatalf("组外读取失败: %+v %v", instances, err)
		}
		if err := store.Merge(delta, at); !errors.Is(err, os.ErrPermission) {
			t.Fatalf("组外写入未被拒绝: %v", err)
		}
	case "write":
		for index := 0; index < count; index++ {
			if err := store.Merge(delta, at); err != nil {
				t.Fatal(err)
			}
		}
	default:
		t.Fatalf("未知测试操作: %s", mode)
	}
}

func assertCatalogGroup(t *testing.T, store Store) {
	t.Helper()
	path := store.Path("project", "instance")
	for name, mode := range map[string]os.FileMode{
		store.Root:         DirectoryMode,
		filepath.Dir(path): DirectoryMode,
		path:               FileMode,
		filepath.Join(filepath.Dir(path), LockName): FileMode,
	} {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		owner := info.Sys().(*syscall.Stat_t)
		if owner.Gid != sharedTestGID || info.Mode()&(os.ModePerm|os.ModeSetgid|os.ModeSticky) != mode {
			t.Fatalf("共享对象的属组或权限不符: %s uid=%d gid=%d mode=%v", name, owner.Uid, owner.Gid, info.Mode())
		}
	}
}

func TestMismatchedGroupsAreNotRepaired(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("不同 GID 由独立 root 测试环境验证")
	}
	for _, kind := range []string{"project", "lock"} {
		t.Run(kind, func(t *testing.T) {
			_, store := preparedGroupStore(t)
			at := time.Now().UTC()
			delta := activeFixture(Instance{Project: "project", InstanceID: "instance", Components: []Component{{Name: "rpc"}}}, at)
			if err := store.Merge(delta, at); err != nil {
				t.Fatal(err)
			}
			path := filepath.Dir(store.Path("project", "instance"))
			mode := DirectoryMode
			if kind == "lock" {
				path = filepath.Join(path, LockName)
				mode = FileMode
			}
			if err := os.Chown(path, -1, sharedTestGID+1); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, mode); err != nil {
				t.Fatal(err)
			}
			if err := store.Merge(delta, at); !errors.Is(err, os.ErrPermission) {
				t.Fatalf("错组对象未被拒绝: %v", err)
			}
			info, err := os.Stat(path)
			if err != nil || info.Sys().(*syscall.Stat_t).Gid != sharedTestGID+1 {
				t.Fatalf("SDK 修复了已有对象的属组: %v", err)
			}
		})
	}
}
