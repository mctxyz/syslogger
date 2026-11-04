package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/mctxyz/syslogger/internal/catalog"
	"github.com/rivo/uniseg"
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
	timestamp := at.Local().Format("2006-01-02 15:04:05")
	want := fmt.Sprintf(`mct-syslogger
├── a
│   ├── local  主网实例
│   │   ├── rpc     %[1]s  累计: 1
│   │   │           区块获取
│   │   └── worker  %[1]s  累计: 1
│   │               后台任务
│   └── other
│       └── worker  %[1]s  累计: 1
│                   后台任务
└── b
    └── local  B 实例
        └── worker  %[1]s  累计: 1
`, timestamp)
	if output.String() != want {
		t.Fatalf("目录树不符:\n%s", output.String())
	}
	output.Reset()
	if err := showCatalog(store, testOptions(t, "--list", "-p", "a", "-i", "local", "-c", "rpc"), &output); err != nil {
		t.Fatal(err)
	}
	want = fmt.Sprintf(`mct-syslogger
└── a
    └── local  主网实例
        └── rpc  %s  累计: 1
                 区块获取
`, timestamp)
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

func TestCatalogComponentCountsAndLocalTime(t *testing.T) {
	now := time.Now().UTC()
	at := time.Date(now.Year(), now.Month(), now.Day()-1, 0, 1, 2, 345678901, time.UTC)
	store := catalog.Store{Root: t.TempDir()}
	components := []catalog.Component{
		{Name: "zero", CreatedAt: at, LastActiveAt: at, LogCount: 0},
		{Name: "maximum", Description: "RPC\t获取", CreatedAt: at, LastActiveAt: at, LogCount: ^uint64(0)},
		{Name: "thousand", CreatedAt: at, LastActiveAt: at, LogCount: 1000},
		{Name: "normal", CreatedAt: at, LastActiveAt: at, LogCount: 11483},
	}
	counts := map[string]string{
		"zero":     "0",
		"maximum":  "18,446,744,073,709,551,615",
		"thousand": "1,000",
		"normal":   "11,483",
	}
	if err := store.Merge(catalog.Instance{Project: "p", InstanceID: "i", Components: components}, now); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, zone := range []struct {
		name   string
		offset int
	}{
		{name: "UTC"},
		{name: "east", offset: 8 * 60 * 60},
		{name: "west", offset: -5 * 60 * 60},
	} {
		t.Run(zone.name, func(t *testing.T) {
			original := time.Local
			time.Local = time.FixedZone(zone.name, zone.offset)
			t.Cleanup(func() {
				time.Local = original
			})
			timestamp := at.UTC().Add(time.Duration(zone.offset) * time.Second).Format("2006-01-02 15:04:05")
			for _, colour := range []bool{false, true} {
				var output bytes.Buffer
				opts := testOptions(t, "--list")
				opts.colour = colour
				if err := showCatalog(store, opts, &output); err != nil {
					t.Fatal(err)
				}
				for _, component := range components {
					name := component.Name
					timeText, countText := timestamp, "累计: "+counts[component.Name]
					if colour {
						name = colourComponent + name + colourReset
						timeText = colourCatalogDetails + timeText + colourReset
						countText = colourCatalogDetails + countText + colourReset
					}
					want := name + strings.Repeat(" ", 8-len(component.Name)+2) + timeText + "  " + countText
					if !strings.Contains(output.String(), want+"\n") {
						t.Fatalf("组件数量、时间格式、时区或亮度不符: want=%q got=%q", want, output.String())
					}
				}
				description := `RPC\t获取`
				if colour {
					description = colourCatalogDetails + description + colourReset
				}
				wantDescription := "        │" + strings.Repeat(" ", 13) + description + "\n"
				if !strings.Contains(output.String(), wantDescription) {
					t.Fatalf("组件描述未另起一行对齐时间或样式不符: %q", output.String())
				}
			}
			var output bytes.Buffer
			if err := showCatalog(store, testOptions(t, "--list", "--json", "--colour"), &output); err != nil {
				t.Fatal(err)
			}
			var decoded []catalog.Instance
			if err := json.Unmarshal(output.Bytes(), &decoded); err != nil || !reflect.DeepEqual(decoded, stored) {
				t.Fatalf("可读显示格式影响了 JSON 目录数据: %s, %v", output.String(), err)
			}
		})
	}
}

func TestCatalogVisibleColumnAlignment(t *testing.T) {
	store := catalog.Store{Root: t.TempDir()}
	at := time.Now().UTC()
	instances := []catalog.Instance{
		{Project: "p", InstanceID: "first", InstanceDescription: "实例说明", Components: []catalog.Component{
			{Name: "a", Description: "description-a"},
			{Name: "中文", Description: "description-chinese"},
			{Name: "e\u0301", Description: "description-combining"},
			{Name: "tab\tname", Description: "description-escaped"},
		}},
		{Project: "q", InstanceID: "second", Components: []catalog.Component{
			{Name: "🌍", Description: "description-emoji"},
		}},
	}
	for _, instance := range instances {
		for index := range instance.Components {
			instance.Components[index].CreatedAt = at
			instance.Components[index].LastActiveAt = at
			instance.Components[index].LogCount = 1
		}
		if err := store.Merge(instance, at); err != nil {
			t.Fatal(err)
		}
	}
	ansi := regexp.MustCompile("\x1b\\[[0-9;]*m")
	timestamp := at.Local().Format("2006-01-02 15:04:05")
	for _, test := range []struct {
		name   string
		args   []string
		column int
	}{
		{name: "all-projects", column: 23},
		{name: "one-component", args: []string{"-c", "a"}, column: 15},
		{name: "one-project", args: []string{"-p", "q"}, column: 16},
	} {
		t.Run(test.name, func(t *testing.T) {
			var plain string
			for _, colour := range []bool{false, true} {
				opts := testOptions(t, append([]string{"--list"}, test.args...)...)
				opts.colour = colour
				var output bytes.Buffer
				if err := showCatalog(store, opts, &output); err != nil {
					t.Fatal(err)
				}
				visible := ansi.ReplaceAllString(output.String(), "")
				if colour && visible != plain {
					t.Fatalf("ANSI 样式改变了对齐或文本:\n%s\n%s", plain, visible)
				}
				plain = visible
				var components, descriptions int
				for _, line := range strings.Split(visible, "\n") {
					if position := strings.Index(line, timestamp); position >= 0 {
						if width := uniseg.StringWidth(line[:position]); width != test.column {
							t.Fatalf("组件时间列不符: width=%d want=%d line=%q", width, test.column, line)
						}
						components++
					}
					if position := strings.Index(line, "description-"); position >= 0 {
						if width := uniseg.StringWidth(line[:position]); width != test.column {
							t.Fatalf("描述与时间未按终端宽度对齐: width=%d want=%d line=%q", width, test.column, line)
						}
						descriptions++
					}
				}
				if components == 0 || components != descriptions {
					t.Fatalf("组件与描述不是逐条两行展示: %s", visible)
				}
			}
		})
	}
}

func TestCatalogDescriptionWriteError(t *testing.T) {
	store := catalog.Store{Root: t.TempDir()}
	at := time.Now().UTC()
	instance := catalog.Instance{Project: "p", InstanceID: "i", Components: []catalog.Component{
		{Name: "rpc", Description: "description", CreatedAt: at, LastActiveAt: at},
	}}
	if err := store.Merge(instance, at); err != nil {
		t.Fatal(err)
	}
	writingErr := errors.New("描述输出失败")
	writes := 0
	writer := callbackWriter{write: func() error {
		writes++
		if writes == 5 {
			return writingErr
		}
		return nil
	}}
	if err := showCatalog(store, testOptions(t, "--list"), writer); !errors.Is(err, writingErr) {
		t.Fatalf("描述行的写入错误未返回: %v", err)
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
