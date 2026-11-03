package catalog

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFixedRoot(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	if Root != "/var/lib/mct-syslogger" {
		t.Fatalf("共享根目录发生变化: %s", Root)
	}
}

// activeFixture 建立带有真实活动字段的目录输入，数量表示本次合并的增量。
func activeFixture(instance Instance, at time.Time) Instance {
	instance.CreatedAt = at
	for index := range instance.Components {
		instance.Components[index].CreatedAt = at
		instance.Components[index].LastActiveAt = at
		instance.Components[index].LogCount = 1
	}
	return instance
}

func TestMergeReadAndAccumulation(t *testing.T) {
	store := Store{Root: filepath.Join(t.TempDir(), "catalog")}
	at := time.Now().UTC()
	instance := activeFixture(Instance{
		Project:             strings.Repeat("项目/", 1000),
		InstanceID:          "../实例\n",
		InstanceDescription: "主网 <监听>",
		Components: []Component{
			{Name: "rpc/连接", Description: "区块与日志\n读取"},
		},
	}, at)
	path := store.Path(instance.Project, instance.InstanceID)
	if filepath.Dir(filepath.Dir(path)) != store.Root || len(filepath.Base(path)) != 69 {
		t.Fatalf("身份没有映射为固定长度的项目目录和实例文件: %s", path)
	}
	if err := store.Merge(instance, at); err != nil {
		t.Fatal(err)
	}
	update := activeFixture(Instance{
		Project: instance.Project, InstanceID: instance.InstanceID, InstanceDescription: "新一次运行",
		Components: []Component{{Name: "rpc/连接", Description: "最新说明"}},
	}, at.Add(time.Minute))
	if err := store.Merge(update, update.CreatedAt); err != nil {
		t.Fatal(err)
	}
	instance.InstanceDescription = update.InstanceDescription
	instance.Components[0].Description = "最新说明"
	instance.Components[0].LastActiveAt = update.CreatedAt
	instance.Components[0].LogCount = 2
	got, err := store.Read()
	if err != nil || !reflect.DeepEqual(got, []Instance{instance}) {
		t.Fatalf("重启合并未保留创建时间或累计量: got=%+v err=%v", got, err)
	}
	if runtime.GOOS != "windows" {
		for path, mode := range map[string]os.FileMode{
			store.Root:         0777,
			filepath.Dir(path): 0777,
			path:               0666,
		} {
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != mode {
				t.Fatalf("公共目录权限不符: path=%s info=%v err=%v", path, info, err)
			}
		}
	}
	files, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(files) != 2 {
		t.Fatalf("发布后存在临时文件: files=%v err=%v", files, err)
	}
}

func TestConcurrentPublicationAndRead(t *testing.T) {
	store := Store{Root: filepath.Join(t.TempDir(), "shared")}
	at := time.Now().UTC()
	var writers sync.WaitGroup
	for _, id := range []string{"instance-a", "instance-b", "instance-c", "instance-a"} {
		writers.Add(1)
		go func(id string) {
			defer writers.Done()
			for i := 0; i < 30; i++ {
				if err := store.Merge(activeFixture(Instance{
					Project:    "project",
					InstanceID: id,
					Components: []Component{{Name: "rpc", Description: strings.Repeat("完整内容", 100)}},
				}, at), at); err != nil {
					t.Error(err)
					return
				}
			}
		}(id)
	}
	for i := 0; i < 50; i++ {
		if err := store.Prune(at); err != nil {
			t.Fatal(err)
		}
		instances, err := store.Read()
		if err != nil {
			t.Error(err)
			break
		}
		for _, instance := range instances {
			if len(instance.Components) != 1 || instance.Components[0].Description != strings.Repeat("完整内容", 100) {
				t.Errorf("读取到了不完整的实例: %+v", instance)
			}
		}
	}
	writers.Wait()
	instances, err := store.Read()
	if err != nil || len(instances) != 3 {
		t.Fatalf("共享目录并发发布丢失实例: got=%+v err=%v", instances, err)
	}
	for _, instance := range instances {
		want := uint64(30)
		if instance.InstanceID == "instance-a" {
			want = 60
		}
		if instance.Components[0].LogCount != want {
			t.Fatalf("同身份并发合并丢失累计量: %+v", instance)
		}
	}
}

func TestMissingAndInvalidCatalog(t *testing.T) {
	store := Store{Root: filepath.Join(t.TempDir(), "catalog")}
	at := time.Now().UTC()
	instances, err := store.Read()
	if err != nil || instances == nil || len(instances) != 0 {
		t.Fatalf("缺省目录应为空列表: got=%v err=%v", instances, err)
	}
	if err := store.Prune(at); err != nil {
		t.Fatal(err)
	}
	if err := store.Merge(Instance{Project: "project", InstanceID: "instance"}, at); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.Root); !os.IsNotExist(err) {
		t.Fatalf("空活动或缺省目录清理产生了文件: %v", err)
	}
	fixture := activeFixture(Instance{Project: "project", InstanceID: "instance", Components: []Component{{Name: "rpc"}}}, at)
	if err := store.Merge(fixture, at); err != nil {
		t.Fatal(err)
	}
	path := store.Path("project", "instance")
	for _, data := range []string{"{invalid", `{"project":"project"}`} {
		if err := os.WriteFile(path, []byte(data), 0666); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Read(); err == nil || !strings.Contains(err.Error(), path) {
			t.Fatalf("无效目录未反馈具体文件: %v", err)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0777); err != nil {
		t.Fatal(err)
	}
	if err := store.Merge(fixture, at); err == nil {
		t.Fatal("目标是目录时发布未失败")
	}
	files, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(files) != 2 {
		t.Fatalf("失败后存在临时文件: %v, %v", files, err)
	}
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, []byte("保留"), 0600); err != nil {
		t.Fatal(err)
	}
	store.Root = blocked
	if err := store.Merge(fixture, at); err == nil {
		t.Fatal("根目录是普通文件时发布未失败")
	}
	if _, err := store.Read(); err == nil {
		t.Fatal("根目录是普通文件时读取未失败")
	}
}

func TestReadSortingAndTemporaryFiles(t *testing.T) {
	store := Store{Root: t.TempDir()}
	at := time.Now().UTC()
	for _, project := range []string{"b", "a"} {
		for _, id := range []string{"z", "a"} {
			if err := store.Merge(activeFixture(Instance{Project: project, InstanceID: id, Components: []Component{{Name: "z"}, {Name: "a"}}}, at), at); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(store.Root, "unrelated"), []byte("保留"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(store.Path("a", "a")), ".catalog-in-progress"), []byte("{partial"), 0600); err != nil {
		t.Fatal(err)
	}
	instances, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, instance := range instances {
		names = append(names, instance.Project+":"+instance.InstanceID)
		if instance.Components[0].Name != "a" {
			t.Fatal("组件未排序")
		}
	}
	if !reflect.DeepEqual(names, []string{"a:a", "a:z", "b:a", "b:z"}) {
		t.Fatalf("项目和实例未排序: %v", names)
	}
	data, err := os.ReadFile(store.Path("a", "a"))
	if err != nil || !json.Valid(data) {
		t.Fatalf("发布文件不是完整 JSON: %v", err)
	}
}

func TestPruneIndividualComponentsAndRevival(t *testing.T) {
	store := Store{Root: t.TempDir()}
	now := time.Now().UTC()
	old := now.AddDate(-1, 0, -1)
	for _, id := range []string{"mixed", "expired"} {
		if err := store.Merge(activeFixture(Instance{Project: "p", InstanceID: id, Components: []Component{{Name: "old"}}}, old), old); err != nil {
			t.Fatal(err)
		}
	}
	recent := now.AddDate(0, -1, 0)
	if err := store.Merge(activeFixture(Instance{Project: "p", InstanceID: "mixed", Components: []Component{{Name: "active"}}}, recent), recent); err != nil {
		t.Fatal(err)
	}
	if err := store.Prune(now); err != nil {
		t.Fatal(err)
	}
	instances, err := store.Read()
	if err != nil || len(instances) != 1 || len(instances[0].Components) != 1 || instances[0].Components[0].Name != "active" || !instances[0].CreatedAt.Equal(old) {
		t.Fatalf("组件独立过期或实例清理不符: %+v, %v", instances, err)
	}
	if err := store.Merge(activeFixture(Instance{Project: "p", InstanceID: "expired", Components: []Component{{Name: "old"}}}, now), now); err != nil {
		t.Fatal(err)
	}
	instances, err = store.Read()
	if err != nil || len(instances) != 2 || !instances[0].CreatedAt.Equal(now) || instances[0].Components[0].LogCount != 1 {
		t.Fatalf("清理后再次活动未建立新记录: %+v, %v", instances, err)
	}
}

func TestMergeOverflowAndExpiryWithoutPrune(t *testing.T) {
	store := Store{Root: t.TempDir()}
	now := time.Now().UTC()
	instance := activeFixture(Instance{Project: "p", InstanceID: "i", Components: []Component{{Name: "rpc"}}}, now)
	instance.Components[0].LogCount = ^uint64(0)
	if err := store.Merge(instance, now); err != nil {
		t.Fatal(err)
	}
	instance.Components[0].LogCount = 1
	if err := store.Merge(instance, now); err != nil {
		t.Fatal(err)
	}
	got, err := store.Read()
	if err != nil || got[0].Components[0].LogCount != 0 || len(got[0].Active(now).Components) != 1 {
		t.Fatalf("溢出未自然回零或零计数组件被误判为空: %+v, %v", got, err)
	}
	later := now.AddDate(1, 0, 1)
	instance = activeFixture(instance, later)
	if err := store.Merge(instance, later); err != nil {
		t.Fatal(err)
	}
	got, err = store.Read()
	if err != nil || got[0].Components[0].LogCount != 1 || !got[0].CreatedAt.Equal(later) {
		t.Fatalf("合并时未重建过期记录: %+v, %v", got, err)
	}
}

func TestMergeAcrossProcesses(t *testing.T) {
	const variable = "SYSLOGGER_CATALOG_TEST_ROOT"
	if root := os.Getenv(variable); root != "" {
		store := Store{Root: root}
		for index := 0; index < 12; index++ {
			at := time.Now().UTC()
			if err := store.Merge(activeFixture(Instance{Project: "p", InstanceID: "i", Components: []Component{{Name: "rpc"}}}, at), at); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	root := t.TempDir()
	var workers sync.WaitGroup
	for index := 0; index < 4; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			command := exec.Command(os.Args[0], "-test.run=^TestMergeAcrossProcesses$")
			command.Env = append(os.Environ(), variable+"="+root)
			if output, err := command.CombinedOutput(); err != nil {
				t.Errorf("子测试合并失败: %s, %v", output, err)
			}
		}()
	}
	workers.Wait()
	instances, err := (Store{Root: root}).Read()
	if err != nil || len(instances) != 1 || instances[0].Components[0].LogCount != 48 {
		t.Fatalf("多进程累计增量丢失: %+v, %v", instances, err)
	}
}

func TestMergeActivityAcrossExpiry(t *testing.T) {
	const variable = "SYSLOGGER_CATALOG_PRUNE_ROOT"
	createdAt := time.Date(2024, 4, 10, 12, 0, 0, 0, time.UTC)
	expiresAt := createdAt.AddDate(1, 0, 0)
	flushAt := expiresAt.Add(time.Minute)
	if root := os.Getenv(variable); root != "" {
		if err := (Store{Root: root}).Prune(flushAt); err != nil {
			t.Fatal(err)
		}
		return
	}
	for _, scenario := range []string{"direct", "prune-other-process", "merge-other-component", "late-without-cleanup"} {
		t.Run(scenario, func(t *testing.T) {
			store := Store{Root: t.TempDir()}
			initial := activeFixture(Instance{Project: "p", InstanceID: "i", Components: []Component{{Name: "rpc"}}}, createdAt)
			initial.Components[0].LogCount = 100
			if err := store.Merge(initial, createdAt); err != nil {
				t.Fatal(err)
			}
			publishedAt := flushAt
			switch scenario {
			case "prune-other-process":
				command := exec.Command(os.Args[0], "-test.run=^TestMergeActivityAcrossExpiry$")
				command.Env = append(os.Environ(), variable+"="+store.Root)
				if output, err := command.CombinedOutput(); err != nil {
					t.Fatalf("另一进程清理失败: %s, %v", output, err)
				}
			case "merge-other-component":
				other := activeFixture(Instance{Project: "p", InstanceID: "i", Components: []Component{{Name: "worker"}}}, flushAt)
				if err := store.Merge(other, flushAt); err != nil {
					t.Fatal(err)
				}
			case "late-without-cleanup":
				publishedAt = expiresAt.Add(2 * SyncInterval)
			}
			activityAt := expiresAt.Add(-time.Minute)
			delta := activeFixture(Instance{Project: "p", InstanceID: "i", Components: []Component{{Name: "rpc"}}}, activityAt)
			if err := store.Merge(delta, publishedAt); err != nil {
				t.Fatal(err)
			}
			instances, err := store.Read()
			if err != nil || len(instances) != 1 || len(instances[0].Components) == 0 {
				t.Fatalf("跨过期点活动丢失记录: %+v, %v", instances, err)
			}
			component := instances[0].Components[0]
			if component.Name != "rpc" || component.LogCount != 101 {
				t.Fatalf("过期前已入队的活动重置了历史数量: %+v", component)
			}
			if !component.CreatedAt.Equal(createdAt) || !instances[0].CreatedAt.Equal(createdAt) || !component.LastActiveAt.Equal(activityAt) {
				t.Fatalf("跨过期点同步改写了生命周期: %+v", instances[0])
			}
		})
	}
}

func TestPhysicalExpiryGracePreservesRecordUntilRemoval(t *testing.T) {
	createdAt := time.Date(2024, 4, 10, 12, 0, 0, 0, time.UTC)
	expiresAt := createdAt.AddDate(1, 0, 0)
	for _, resume := range []bool{false, true} {
		store := Store{Root: t.TempDir()}
		initial := activeFixture(Instance{Project: "p", InstanceID: "i", Components: []Component{{Name: "rpc"}}}, createdAt)
		initial.Components[0].LogCount = 100
		if err := store.Merge(initial, createdAt); err != nil {
			t.Fatal(err)
		}
		at := expiresAt.Add(time.Minute)
		if err := store.Prune(at); err != nil {
			t.Fatal(err)
		}
		instances, err := store.Read()
		if err != nil || len(instances) != 1 || len(instances[0].Active(at).Components) != 0 {
			t.Fatalf("物理宽限改变了逻辑过期或提前删除了记录: %+v, %v", instances, err)
		}
		if resume {
			delta := activeFixture(Instance{Project: "p", InstanceID: "i", Components: []Component{{Name: "rpc"}}}, at)
			if err := store.Merge(delta, at); err != nil {
				t.Fatal(err)
			}
			instances, err = store.Read()
			if err != nil || instances[0].Components[0].LogCount != 101 || !instances[0].Components[0].CreatedAt.Equal(createdAt) {
				t.Fatalf("宽限期间提前重置了历史记录: %+v, %v", instances, err)
			}
		} else {
			if err := store.Prune(expiresAt.Add(SyncInterval + time.Nanosecond)); err != nil {
				t.Fatal(err)
			}
			instances, err = store.Read()
			if err != nil || len(instances) != 0 {
				t.Fatalf("同步窗口结束后没有物理清理: %+v, %v", instances, err)
			}
		}
	}
}

func TestMergeSameComponentOutOfOrderAcrossExpiry(t *testing.T) {
	createdAt := time.Date(2024, 4, 10, 12, 0, 0, 0, time.UTC)
	expiresAt := createdAt.AddDate(1, 0, 0)
	early := expiresAt.Add(-time.Minute)
	late := expiresAt.Add(time.Minute)
	for _, order := range []struct {
		name  string
		times []time.Time
	}{
		{name: "early-first", times: []time.Time{early, late}},
		{name: "late-first", times: []time.Time{late, early}},
	} {
		t.Run(order.name, func(t *testing.T) {
			store := Store{Root: t.TempDir()}
			initial := activeFixture(Instance{Project: "p", InstanceID: "i", Components: []Component{{Name: "rpc"}}}, createdAt)
			initial.Components[0].LogCount = 100
			if err := store.Merge(initial, createdAt); err != nil {
				t.Fatal(err)
			}
			for index, at := range order.times {
				delta := activeFixture(Instance{Project: "p", InstanceID: "i", Components: []Component{{Name: "rpc"}}}, at)
				if err := store.Merge(delta, expiresAt.Add(time.Duration(index+2)*time.Minute)); err != nil {
					t.Fatal(err)
				}
				if err := store.Prune(expiresAt.Add(time.Duration(index+2) * time.Minute)); err != nil {
					t.Fatal(err)
				}
			}
			instances, err := store.Read()
			if err != nil || len(instances) != 1 || len(instances[0].Components) != 1 {
				t.Fatalf("逆序合并丢失组件: %+v, %v", instances, err)
			}
			component := instances[0].Components[0]
			if component.LogCount != 102 || !component.CreatedAt.Equal(createdAt) || !component.LastActiveAt.Equal(late) {
				t.Fatalf("同组件批次的落盘顺序改变了历史累计: %+v", component)
			}
			if !instances[0].CreatedAt.Equal(createdAt) {
				t.Fatalf("逆序合并重置了实例创建时间: %+v", instances[0])
			}
		})
	}
}
