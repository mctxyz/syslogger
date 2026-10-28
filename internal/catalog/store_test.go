package catalog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestFixedRoot(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	if Root != "/tmp/mct-syslogger" {
		t.Fatalf("共享根目录发生变化: %s", Root)
	}
}

func TestWriteReadAndReplacement(t *testing.T) {
	store := Store{Root: filepath.Join(t.TempDir(), "catalog")}
	instance := Instance{
		Project:             strings.Repeat("项目/", 1000),
		InstanceID:          "../实例\n",
		InstanceDescription: "主网 <监听>",
		Components: []Component{
			{Name: "rpc/连接", Description: "区块与日志\n读取"},
		},
	}
	path := store.Path(instance.Project, instance.InstanceID)
	if filepath.Dir(filepath.Dir(path)) != store.Root || len(filepath.Base(path)) != 69 {
		t.Fatalf("身份没有映射为固定长度的项目目录和实例文件: %s", path)
	}
	for _, expected := range []Instance{instance, {
		Project:             instance.Project,
		InstanceID:          instance.InstanceID,
		InstanceDescription: "新一次运行",
		Components:          []Component{},
	}} {
		if err := store.Write(expected); err != nil {
			t.Fatal(err)
		}
		got, err := store.Read()
		if err != nil || !reflect.DeepEqual(got, []Instance{expected}) {
			t.Fatalf("目录内容不符: got=%+v err=%v", got, err)
		}
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
	if err != nil || len(files) != 1 {
		t.Fatalf("发布后存在临时文件: files=%v err=%v", files, err)
	}
}

func TestConcurrentPublicationAndRead(t *testing.T) {
	store := Store{Root: filepath.Join(t.TempDir(), "shared")}
	var writers sync.WaitGroup
	for _, id := range []string{"instance-a", "instance-b", "instance-c", "instance-a"} {
		writers.Add(1)
		go func(id string) {
			defer writers.Done()
			for i := 0; i < 30; i++ {
				if err := store.Write(Instance{
					Project:    "project",
					InstanceID: id,
					Components: []Component{{Name: "rpc", Description: strings.Repeat("完整内容", 100)}},
				}); err != nil {
					t.Error(err)
					return
				}
			}
		}(id)
	}
	for i := 0; i < 50; i++ {
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
}

func TestMissingAndInvalidCatalog(t *testing.T) {
	store := Store{Root: filepath.Join(t.TempDir(), "catalog")}
	instances, err := store.Read()
	if err != nil || instances == nil || len(instances) != 0 {
		t.Fatalf("缺省目录应为空列表: got=%v err=%v", instances, err)
	}
	if err := store.Write(Instance{Project: "project", InstanceID: "instance", Components: []Component{}}); err != nil {
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
	if err := store.Write(Instance{Project: "project", InstanceID: "instance"}); err == nil {
		t.Fatal("目标是目录时发布未失败")
	}
	files, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(files) != 1 {
		t.Fatalf("失败后存在临时文件: %v, %v", files, err)
	}
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, []byte("保留"), 0600); err != nil {
		t.Fatal(err)
	}
	store.Root = blocked
	if err := store.Write(Instance{Project: "project", InstanceID: "instance"}); err == nil {
		t.Fatal("根目录是普通文件时发布未失败")
	}
	if _, err := store.Read(); err == nil {
		t.Fatal("根目录是普通文件时读取未失败")
	}
}

func TestReadSortingAndTemporaryFiles(t *testing.T) {
	store := Store{Root: t.TempDir()}
	for _, project := range []string{"b", "a"} {
		for _, id := range []string{"z", "a"} {
			if err := store.Write(Instance{Project: project, InstanceID: id, Components: []Component{{Name: "z"}, {Name: "a"}}}); err != nil {
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
