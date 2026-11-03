package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mctxyz/syslogger/internal/catalog"
)

func TestCatalogOptions(t *testing.T) {
	opts := testOptions(t, "--list", "-p", "a", "-p", "b", "-i", "local", "-c", "rpc", "--json", "--colour")
	if !opts.list || !opts.asJSON || !opts.colour || len(opts.projects) != 2 {
		t.Fatal("目录参数没有生效")
	}
	if len(opts.instances) != 1 || opts.instances[0] != "local" {
		t.Fatalf("目录模式的 -i 未作为实例筛选生效：%v", opts.instances)
	}
	for _, args := range [][]string{
		{"--list", "-f"},
		{"--list", "--since=now"},
		{"--list", "--until=now"},
		{"--list", "-n", "0"},
		{"--list", "-l", "error"},
		{"--list", "--level", "debug"},
		{"--list", "--include-level", "error"},
		{"--list", "--exclude-level", "debug"},
		{"--list", "-k", "rpc"},
		{"--list", "-e", "rpc"},
		{"--list", "--ignore-case"},
		{"--list", "--debug"},
		{"--list", "--debug=false"},
	} {
		if _, err := parseOptions(args, io.Discard); err == nil {
			t.Fatalf("目录查询静默忽略了日志参数: %v", args)
		}
	}
}

func TestCatalogTreeAndFiltering(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	store := catalog.Store{Root: t.TempDir()}
	at := time.Now().UTC()
	for _, instance := range []catalog.Instance{
		{Project: "b", InstanceID: "local", InstanceDescription: "B 实例", Components: []catalog.Component{{Name: "worker"}}},
		{Project: "a", InstanceID: "other", Components: []catalog.Component{{Name: "worker", Description: "后台任务"}}},
		{Project: "a", InstanceID: "local", InstanceDescription: "主网实例", Components: []catalog.Component{{Name: "worker", Description: "后台任务"}, {Name: "rpc", Description: "区块获取"}}},
	} {
		for index := range instance.Components {
			instance.Components[index].CreatedAt = at
			instance.Components[index].LastActiveAt = at
			instance.Components[index].LogCount = 1
		}
		if err := store.Merge(instance, at); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	if err := showCatalog(store, testOptions(t, "--list"), &output); err != nil {
		t.Fatal(err)
	}
	want := `mct-syslogger
├── a
│   ├── local  主网实例
│   │   ├── rpc  区块获取
│   │   └── worker  后台任务
│   └── other
│       └── worker  后台任务
└── b
    └── local  B 实例
        └── worker
`
	if output.String() != want {
		t.Fatalf("目录树不符:\n%s", output.String())
	}
	output.Reset()
	if err := showCatalog(store, testOptions(t, "--list", "-p", "a", "-i", "local", "-c", "rpc"), &output); err != nil {
		t.Fatal(err)
	}
	want = `mct-syslogger
└── a
    └── local  主网实例
        └── rpc  区块获取
`
	if output.String() != want {
		t.Fatalf("单项目筛选后的目录树不符:\n%s", output.String())
	}
	for _, args := range [][]string{
		{"--list", "-p", "a", "-i", "local", "-c", "rpc", "--json"},
		{"--list", "-p", "missing", "-p", "a", "-i", "local", "-c", "rpc", "--json"},
	} {
		output.Reset()
		if err := showCatalog(store, testOptions(t, args...), &output); err != nil {
			t.Fatal(err)
		}
		var got []catalog.Instance
		if err := json.Unmarshal(output.Bytes(), &got); err != nil || len(got) != 1 || got[0].Project != "a" || got[0].InstanceID != "local" || len(got[0].Components) != 1 || got[0].Components[0].Name != "rpc" {
			t.Fatalf("目录筛选结果不符: %s, %v", output.String(), err)
		}
	}
	output.Reset()
	if err := showCatalog(store, testOptions(t, "--list", "-c", "absent", "--json"), &output); err != nil || output.String() != "[]\n" {
		t.Fatalf("无匹配组件时应返回空列表: %q, %v", output.String(), err)
	}
	output.Reset()
	if err := showCatalog(store, testOptions(t, "--list", "-c", "absent"), &output); err != nil || output.Len() != 0 {
		t.Fatalf("无匹配组件时应保持空输出: %q, %v", output.String(), err)
	}
}

func TestCatalogEscapingColourAndErrors(t *testing.T) {
	store := catalog.Store{Root: filepath.Join(t.TempDir(), "missing")}
	var output bytes.Buffer
	if err := showCatalog(store, testOptions(t, "--list", "--json"), &output); err != nil || output.String() != "[]\n" {
		t.Fatalf("缺省目录查询失败: %q, %v", output.String(), err)
	}
	instance := catalog.Instance{Project: "a\nproject", InstanceID: "local\tinstance", InstanceDescription: "描述\n\x1b[31m", Components: []catalog.Component{{Name: "rpc", Description: "RPC\t获取"}}}
	at := time.Now().UTC()
	instance.Components[0].CreatedAt = at
	instance.Components[0].LastActiveAt = at
	instance.Components[0].LogCount = 1
	if err := store.Merge(instance, at); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := showCatalog(store, testOptions(t, "--list"), &output); err != nil || strings.ContainsAny(output.String(), "\t\x1b") || !strings.Contains(output.String(), `a\nproject`) {
		t.Fatalf("可读目录没有转义控制字符: %q, %v", output.String(), err)
	}
	output.Reset()
	if err := showCatalog(store, testOptions(t, "--list", "--colour"), &output); err != nil || !strings.Contains(output.String(), colourProject) {
		t.Fatalf("目录着色失败: %q, %v", output.String(), err)
	}
	output.Reset()
	if err := showCatalog(store, testOptions(t, "--list", "--colour", "--json"), &output); err != nil || !json.Valid(output.Bytes()) {
		t.Fatalf("JSON 目录受颜色影响: %q, %v", output.String(), err)
	}
	writingErr := errors.New("输出关闭")
	for _, args := range [][]string{{"--list"}, {"--list", "--json"}} {
		if err := showCatalog(store, testOptions(t, args...), callbackWriter{write: func() error { return writingErr }}); !errors.Is(err, writingErr) {
			t.Fatalf("目录输出错误未透传: %v", err)
		}
	}
	if err := os.WriteFile(store.Path(instance.Project, instance.InstanceID), []byte("{broken"), 0666); err != nil {
		t.Fatal(err)
	}
	if err := showCatalog(store, testOptions(t, "--list"), io.Discard); err == nil {
		t.Fatal("无效目录未报错")
	}
}

func TestCatalogExpiryIsReadOnlyAndPreservesUint64(t *testing.T) {
	store := catalog.Store{Root: t.TempDir()}
	now := time.Now().UTC()
	old := now.AddDate(-1, 0, 0).Add(-time.Minute)
	for _, id := range []string{"mixed", "expired"} {
		if err := store.Merge(catalog.Instance{
			Project: "p", InstanceID: id,
			Components: []catalog.Component{{Name: "old", CreatedAt: old, LastActiveAt: old, LogCount: 7}},
		}, old); err != nil {
			t.Fatal(err)
		}
	}
	recent := now.AddDate(0, -1, 0)
	if err := store.Merge(catalog.Instance{
		Project: "p", InstanceID: "mixed",
		Components: []catalog.Component{{Name: "active", CreatedAt: recent, LastActiveAt: recent, LogCount: ^uint64(0)}},
	}, recent); err != nil {
		t.Fatal(err)
	}
	if err := store.Prune(now); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := showCatalog(store, testOptions(t, "--list", "--json"), &output); err != nil {
		t.Fatal(err)
	}
	var selected []catalog.Instance
	if err := json.Unmarshal(output.Bytes(), &selected); err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || len(selected[0].Components) != 1 {
		t.Fatalf("目录过期过滤不符: %s", output.String())
	}
	component := selected[0].Components[0]
	if component.Name != "active" || component.LogCount != ^uint64(0) || !component.LastActiveAt.Equal(recent) {
		t.Fatalf("目录元数据或 uint64 精度不符: %+v", component)
	}
	instances, err := store.Read()
	if err != nil || len(instances) != 2 || len(instances[1].Components) != 2 {
		t.Fatalf("只读查询改写了目录文件: %+v, %v", instances, err)
	}
}
