package hostinit

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mctxyz/syslogger/internal/catalog"
	"golang.org/x/sys/unix"
)

func TestCatalogRejectsUnsafeManagedObjects(t *testing.T) {
	for _, kind := range []string{"project-link", "project-file", "json-link", "lock-link", "json-hardlink", "lock-hardlink", "json-fifo", "json-directory", "json-acl", "project-default-acl"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			project := filepath.Join(root, strings.Repeat("a", 64))
			outside := t.TempDir()
			sentinel := filepath.Join(outside, "preserve")
			if err := os.WriteFile(sentinel, []byte("外部对象"), 0600); err != nil {
				t.Fatal(err)
			}
			beforeFile, err := os.Stat(sentinel)
			if err != nil {
				t.Fatal(err)
			}
			beforeDirectory, err := os.Stat(outside)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "project-link" {
				if err := os.Symlink(outside, project); err != nil {
					t.Fatal(err)
				}
			} else if kind == "project-file" {
				if err := os.WriteFile(project, []byte("保留"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(project, 0700); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(project, strings.Repeat("b", 64)+".json")
				if strings.HasPrefix(kind, "lock-") {
					path = filepath.Join(project, catalog.LockName)
				}
				switch kind {
				case "json-link", "lock-link":
					if err := os.Symlink(sentinel, path); err != nil {
						t.Fatal(err)
					}
				case "json-hardlink", "lock-hardlink":
					if err := os.Link(sentinel, path); err != nil {
						t.Fatal(err)
					}
				case "json-fifo":
					if err := unix.Mkfifo(path, 0600); err != nil {
						t.Fatal(err)
					}
				case "json-directory":
					if err := os.Mkdir(path, 0700); err != nil {
						t.Fatal(err)
					}
				case "json-acl":
					if err := os.WriteFile(path, []byte("保留"), 0600); err != nil {
						t.Fatal(err)
					}
					setCatalogTestACL(t, path, "system.posix_acl_access")
				case "project-default-acl":
					setCatalogTestACL(t, project, "system.posix_acl_default")
				}
			}
			if err := testInitializer(io.Discard).prepareCatalog(root, os.Geteuid(), os.Getegid()); err == nil {
				t.Fatalf("没有拒绝不安全的受管对象: %s", kind)
			} else if strings.Contains(kind, "acl") && !strings.Contains(err.Error(), "ACL") {
				t.Fatalf("未实际检查受管对象 ACL: %v", err)
			}
			for path, before := range map[string]os.FileInfo{sentinel: beforeFile, outside: beforeDirectory} {
				after, err := os.Stat(path)
				if err != nil || !os.SameFile(before, after) || after.Mode() != before.Mode() {
					t.Fatalf("修复越界修改了外部对象: %s %v", path, err)
				}
			}
			data, err := os.ReadFile(sentinel)
			if err != nil || string(data) != "外部对象" {
				t.Fatalf("修复改写了外部文件内容: %q %v", data, err)
			}
		})
	}
}

func setCatalogTestACL(t *testing.T, path, name string) {
	t.Helper()
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
	if err := unix.Setxattr(path, name, acl, 0); errors.Is(err, unix.ENOTSUP) {
		t.Skip("测试文件系统不支持 POSIX ACL")
	} else if err != nil {
		t.Fatal(err)
	}
}

// TestCatalogRepairOwnership 验证旧身份迁移、重复初始化及迁移后不同用户的实际 SDK 写入。
func TestCatalogRepairOwnership(t *testing.T) {
	const variable = "SYSLOGGER_CATALOG_REPAIR_WORKER"
	const gid = 12345
	at := time.Date(2026, 11, 10, 1, 0, 0, 0, time.UTC)
	if directory := os.Getenv(variable); directory != "" {
		if err := os.Chdir(directory); err != nil {
			t.Fatal(err)
		}
		uid, err := strconv.Atoi(os.Getenv("SYSLOGGER_CATALOG_REPAIR_UID"))
		if err != nil {
			t.Fatal(err)
		}
		if err := unix.Setgroups([]int{gid}); err != nil {
			t.Fatal(err)
		}
		if err := unix.Setgid(20000 + uid); err != nil {
			t.Fatal(err)
		}
		if err := unix.Setuid(uid); err != nil {
			t.Fatal(err)
		}
		groups, err := os.Getgroups()
		if err != nil || os.Geteuid() != uid || os.Getegid() != 20000+uid || len(groups) != 1 || groups[0] != gid {
			t.Fatalf("迁移验证的子进程身份不符: uid=%d gid=%d groups=%v %v", os.Geteuid(), os.Getegid(), groups, err)
		}
		unix.Umask(077)
		instance := catalog.Instance{Project: "project", InstanceID: "instance", CreatedAt: at, Components: []catalog.Component{{Name: "rpc", CreatedAt: at, LastActiveAt: at}}}
		instance.Components[0].LogCounts.Add(at, 1)
		store := catalog.Store{Root: "catalog"}
		if err := store.Merge(instance, at); err != nil {
			t.Fatal(err)
		}
		if err := store.Prune(at); err != nil {
			t.Fatal(err)
		}
		return
	}
	if os.Geteuid() != 0 {
		t.Skip("跨 UID 迁移由独立 root 测试环境验证")
	}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0755); err != nil {
		t.Fatal(err)
	}
	store := catalog.Store{Root: filepath.Join(directory, "catalog")}
	path := store.Path("project", "instance")
	project := filepath.Dir(path)
	if err := os.MkdirAll(project, 0700); err != nil {
		t.Fatal(err)
	}
	instance := catalog.Instance{Project: "project", InstanceID: "instance", CreatedAt: at, Components: []catalog.Component{{Name: "rpc", CreatedAt: at, LastActiveAt: at}}}
	instance.Components[0].LogCounts.Add(at, 5)
	data, err := json.Marshal(instance)
	if err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(project, catalog.LockName)
	for name, contents := range map[string][]byte{path: data, lock: nil} {
		if err := os.WriteFile(name, contents, 0600); err != nil {
			t.Fatal(err)
		}
	}
	before := make(map[string]os.FileInfo)
	for name, uid := range map[string]int{store.Root: 1001, project: 1002, path: 1002, lock: 1001} {
		if err := os.Chown(name, uid, 22000+uid); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		before[name] = info
	}
	init := testInitializer(io.Discard)
	for attempt := 0; attempt < 3; attempt++ {
		if err := init.prepareCatalog(store.Root, 0, gid); err != nil {
			t.Fatal(err)
		}
		for name, previous := range before {
			info, err := os.Stat(name)
			if err != nil {
				t.Fatal(err)
			}
			owner := info.Sys().(*syscall.Stat_t)
			wantUID := previous.Sys().(*syscall.Stat_t).Uid
			mode := os.FileMode(catalog.FileMode)
			if info.IsDir() {
				wantUID = 0
				mode = catalog.DirectoryMode
			}
			if owner.Uid != wantUID || owner.Gid != gid || info.Mode()&(os.ModePerm|os.ModeSetgid|os.ModeSticky) != mode || !os.SameFile(previous, info) {
				t.Fatalf("迁移或重复初始化破坏了对象身份: %s uid=%d gid=%d mode=%v", name, owner.Uid, owner.Gid, info.Mode())
			}
		}
		contents, err := os.ReadFile(path)
		if err != nil || string(contents) != string(data) {
			t.Fatalf("迁移重写了清单: %v", err)
		}
	}
	for _, uid := range []int{1001, 1002} {
		command := exec.Command(os.Args[0], "-test.run=^TestCatalogRepairOwnership$")
		command.Env = append(os.Environ(), variable+"="+directory, "SYSLOGGER_CATALOG_REPAIR_UID="+strconv.Itoa(uid))
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("迁移后组内用户无法维护目录: uid=%d %s %v", uid, output, err)
		}
	}
	instances, err := store.Read()
	if err != nil || len(instances) != 1 {
		t.Fatalf("迁移后 SDK 读取失败: %+v %v", instances, err)
	}
	var count uint64
	for _, bucket := range instances[0].Components[0].LogCounts {
		count += bucket.Count
	}
	if count != 7 {
		t.Fatalf("迁移后 SDK 累计量丢失: %d", count)
	}
	after, err := os.Stat(lock)
	if err != nil || !os.SameFile(before[lock], after) {
		t.Fatalf("迁移后 SDK 替换了稳定锁: %v", err)
	}
}
