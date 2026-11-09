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
	_ "time/tzdata"

	"github.com/mctxyz/syslogger/internal/catalog"
	"github.com/mctxyz/syslogger/internal/hourly"
	"github.com/rivo/uniseg"
)

// preparedCatalogRoot 为 SDK 发布夹具准备管理员负责的共享根目录。
func preparedCatalogRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, catalog.DirectoryMode); err != nil {
		t.Fatal(err)
	}
	return root
}

func catalogCounts(at time.Time, count uint64) hourly.Window {
	var window hourly.Window
	window.Add(at, count)
	return window
}

func catalogCountValue(component catalog.Component) uint64 {
	var count uint64
	for _, bucket := range component.LogCounts {
		count += bucket.Count
	}
	return count
}

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
	} {
		if _, err := parseOptions(args, io.Discard); err == nil {
			t.Fatalf("目录查询静默忽略了日志参数: %v", args)
		}
	}
}

func TestCatalogTreeAndFiltering(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	store := catalog.Store{Root: preparedCatalogRoot(t)}
	at := time.Now().UTC()
	for _, instance := range []catalog.Instance{
		{Project: "b", InstanceID: "local", InstanceDescription: "B 实例", Components: []catalog.Component{{Name: "worker"}}},
		{Project: "a", InstanceID: "other", Components: []catalog.Component{{Name: "worker", Description: "后台任务"}}},
		{Project: "a", InstanceID: "local", InstanceDescription: "主网实例", Components: []catalog.Component{{Name: "worker", Description: "后台任务"}, {Name: "rpc", Description: "区块获取"}}},
	} {
		for index := range instance.Components {
			instance.Components[index].CreatedAt = at
			instance.Components[index].LastActiveAt = at
			instance.Components[index].LogCounts = catalogCounts(at, 1)
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
	// 小时起点由查询时刻决定；目录结构单独验证，具体计数由下方时区用例验证。
	countText := regexp.MustCompile(`每小时:[^\n]*`)
	want := fmt.Sprintf(`mct-syslogger
├── a
│   ├── local  主网实例
│   │   ├── rpc     %[1]s  每小时: <hours>
│   │   │           区块获取
│   │   └── worker  %[1]s  每小时: <hours>
│   │               后台任务
│   └── other
│       └── worker  %[1]s  每小时: <hours>
│                   后台任务
└── b
    └── local  B 实例
        └── worker  %[1]s  每小时: <hours>
`, timestamp)
	if countText.ReplaceAllString(output.String(), "每小时: <hours>") != want {
		t.Fatalf("目录树不符:\n%s", output.String())
	}
	output.Reset()
	if err := showCatalog(store, testOptions(t, "--list", "-p", "a", "-i", "local", "-c", "rpc"), &output); err != nil {
		t.Fatal(err)
	}
	want = fmt.Sprintf(`mct-syslogger
└── a
    └── local  主网实例
        └── rpc  %s  每小时: <hours>
                 区块获取
`, timestamp)
	if countText.ReplaceAllString(output.String(), "每小时: <hours>") != want {
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
	counts := map[string]string{
		"zero":     "0",
		"maximum":  "18,446,744,073,709,551,615",
		"thousand": "1,000",
		"normal":   "11,483",
	}
	ansi := regexp.MustCompile("\x1b\\[[0-9;]*m")
	for _, zone := range []struct {
		name   string
		offset int
	}{
		{name: "UTC"},
		{name: "east", offset: 8 * 60 * 60},
		{name: "west", offset: -5 * 60 * 60},
		{name: "fractional", offset: 5*60*60 + 45*60},
	} {
		t.Run(zone.name, func(t *testing.T) {
			original := time.Local
			time.Local = time.FixedZone(zone.name, zone.offset)
			t.Cleanup(func() {
				time.Local = original
			})
			// SDK 与 CLI 共用设备时区，先设置本例时区再创建该时区的小时桶。
			now := time.Now().UTC()
			at := now.Add(-time.Minute)
			store := catalog.Store{Root: preparedCatalogRoot(t)}
			components := []catalog.Component{
				{Name: "zero", CreatedAt: at, LastActiveAt: at, LogCounts: catalogCounts(at, 0)},
				{Name: "maximum", Description: "RPC\t获取", CreatedAt: at, LastActiveAt: at, LogCounts: catalogCounts(at, ^uint64(0))},
				{Name: "thousand", CreatedAt: at, LastActiveAt: at, LogCounts: catalogCounts(at, 1000)},
				{Name: "normal", CreatedAt: at, LastActiveAt: at, LogCounts: catalogCounts(at, 11483)},
			}
			if err := store.Merge(catalog.Instance{
				Project:             "p",
				InstanceID:          "i",
				InstanceDescription: "实例说明",
				Components:          components,
			}, now); err != nil {
				t.Fatal(err)
			}
			stored, err := store.Read()
			if err != nil {
				t.Fatal(err)
			}
			timestamp := at.Local().Format("2006-01-02 15:04:05")
			for _, colour := range []bool{false, true} {
				var output bytes.Buffer
				opts := testOptions(t, "--list")
				opts.colour = colour
				if err := showCatalog(store, opts, &output); err != nil {
					t.Fatal(err)
				}
				instanceName := "i"
				if colour {
					instanceName = colourInstance + instanceName + colourReset
				}
				if !strings.Contains(output.String(), instanceName+"  实例说明\n") {
					t.Fatalf("实例描述未保持默认前景色及正常亮度：%q", output.String())
				}
				for _, component := range components {
					name := component.Name
					timeText := timestamp
					if colour {
						name = colourComponent + name + colourReset
						timeText = colourCatalogDetails + timeText + colourReset
					}
					prefix := name + strings.Repeat(" ", 8-len(component.Name)+2) + timeText + "  "
					var countText string
					for _, line := range strings.Split(output.String(), "\n") {
						_, content, hasBranch := strings.Cut(line, "── ")
						if hasBranch && strings.HasPrefix(content, prefix) {
							countText = strings.TrimPrefix(content, prefix)
							break
						}
					}
					if colour && (!strings.HasPrefix(countText, colourCatalogDetails) || !strings.HasSuffix(countText, colourReset)) {
						t.Fatalf("小时计数未使用默认前景色弱化亮度：%q", countText)
					}
					plain := ansi.ReplaceAllString(countText, "")
					parts := strings.Split(strings.TrimPrefix(plain, "每小时: "), " | ")
					if !strings.HasPrefix(plain, "每小时: ") || len(parts) != hourly.Hours {
						t.Fatalf("组件数量、时间格式或时区不符：prefix=%q got=%q", prefix, output.String())
					}
					start := hourly.Start(at).Local()
					wanted := start.Format("15:04") + "=" + counts[component.Name]
					dated := start.Format("01-02 15:04") + "=" + counts[component.Name]
					var found bool
					for _, part := range parts {
						if part == wanted || part == dated {
							found = true
						} else if !strings.HasSuffix(part, "=0") {
							t.Fatalf("未接收日志的小时出现数量：%q", part)
						}
					}
					if !found {
						t.Fatalf("小时起点或完整整数格式不符：want=%q got=%q", wanted, plain)
					}
				}
				description := `RPC\t获取`
				wantDescription := "        │" + strings.Repeat(" ", 13) + description + "\n"
				if !strings.Contains(output.String(), wantDescription) {
					t.Fatalf("组件描述未对齐时间，或未与实例描述保持相同样式: %q", output.String())
				}
			}
			var output bytes.Buffer
			beforeQuery := time.Now()
			if err := showCatalog(store, testOptions(t, "--list", "--json", "--colour"), &output); err != nil {
				t.Fatal(err)
			}
			var decoded []catalog.Instance
			if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
				t.Fatal(err)
			}
			if len(decoded) != 1 || len(decoded[0].Components) != len(components) {
				t.Fatalf("JSON 目录丢失身份：%s", output.String())
			}
			base := decoded[0].Components[0].LogCounts[hourly.Hours-1].StartTime
			if !base.Equal(hourly.Start(beforeQuery)) && !base.Equal(hourly.Start(time.Now())) {
				t.Fatalf("JSON 目录没有使用查询当时的小时窗口：%v", base)
			}
			for index := range stored[0].Components {
				stored[0].Components[index].LogCounts = stored[0].Components[index].LogCounts.Recent(base)
			}
			if !reflect.DeepEqual(decoded, stored) {
				t.Fatalf("JSON 小时视图或身份元数据不符：%s", output.String())
			}
		})
	}
}

func TestCatalogCompactHourCounts(t *testing.T) {
	east := time.FixedZone("east", 8*60*60)
	fractional := time.FixedZone("fractional", 5*60*60+45*60)
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	lordHowe, err := time.LoadLocation("Australia/Lord_Howe")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		location *time.Location
		at       time.Time
		want     string
	}{
		{
			name:     "same-day",
			location: east,
			at:       time.Date(2026, 11, 6, 18, 25, 0, 0, east),
			want:     "每小时: 16:00=0 | 17:00=4,905 | 18:00=1,842",
		},
		{
			name:     "fractional-offset",
			location: fractional,
			at:       time.Date(2026, 11, 6, 18, 25, 0, 0, fractional),
			want:     "每小时: 16:00=0 | 17:00=4,905 | 18:00=1,842",
		},
		{
			name:     "midnight",
			location: east,
			at:       time.Date(2026, 11, 7, 0, 25, 0, 0, east),
			want:     "每小时: 11-06 22:00=0 | 11-06 23:00=4,905 | 11-07 00:00=1,842",
		},
		{
			name:     "new-year",
			location: east,
			at:       time.Date(2027, 1, 1, 0, 25, 0, 0, east),
			want:     "每小时: 12-31 22:00=0 | 12-31 23:00=4,905 | 01-01 00:00=1,842",
		},
		{
			name:     "repeated-hour",
			location: newYork,
			at:       time.Date(2026, 11, 1, 6, 25, 0, 0, time.UTC),
			want:     "每小时: 00:00 -0400=0 | 01:00 -0400=4,905 | 01:00 -0500=1,842",
		},
		{
			name:     "partial-hour",
			location: lordHowe,
			at:       time.Date(2026, 10, 3, 15, 45, 0, 0, time.UTC),
			want:     "每小时: 00:00 +1030=0 | 01:00 +1030=4,905 | 02:30 +1100=1,842",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := time.Local
			time.Local = test.location
			t.Cleanup(func() { time.Local = original })
			window := (hourly.Window{}).Recent(test.at)
			for index, count := range [...]uint64{0, 4905, 1842} {
				window[index].Count = count
			}
			before := window
			if got := catalogHourCounts(window); got != test.want {
				t.Fatalf("紧凑小时数量格式不符：got=%q want=%q", got, test.want)
			}
			if window != before {
				t.Fatal("显示格式修改了原始时间或数量")
			}
		})
	}
}

func TestCatalogVisibleColumnAlignment(t *testing.T) {
	store := catalog.Store{Root: preparedCatalogRoot(t)}
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
			instance.Components[index].LogCounts = catalogCounts(at, 1)
		}
		if err := store.Merge(instance, at); err != nil {
			t.Fatal(err)
		}
	}
	ansi := regexp.MustCompile("\x1b\\[[0-9;]*m")
	hourCounts := regexp.MustCompile(`每小时:[^\n]*`)
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
				layout := hourCounts.ReplaceAllString(visible, "每小时: <hours>")
				if colour && layout != plain {
					t.Fatalf("ANSI 样式改变了对齐或文本:\n%s\n%s", plain, layout)
				}
				plain = layout
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
	store := catalog.Store{Root: preparedCatalogRoot(t)}
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
	instance.Components[0].LogCounts = catalogCounts(at, 1)
	if err := os.Mkdir(store.Root, catalog.DirectoryMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store.Root, catalog.DirectoryMode); err != nil {
		t.Fatal(err)
	}
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
	store := catalog.Store{Root: preparedCatalogRoot(t)}
	now := time.Now().UTC()
	old := now.AddDate(-1, 0, 0).Add(-time.Minute)
	for _, id := range []string{"mixed", "expired"} {
		if err := store.Merge(catalog.Instance{
			Project: "p", InstanceID: id,
			Components: []catalog.Component{{Name: "old", CreatedAt: old, LastActiveAt: old, LogCounts: catalogCounts(old, 7)}},
		}, old); err != nil {
			t.Fatal(err)
		}
	}
	recent := now.Add(-time.Hour)
	if err := store.Merge(catalog.Instance{
		Project: "p", InstanceID: "mixed",
		Components: []catalog.Component{{Name: "active", CreatedAt: recent, LastActiveAt: recent, LogCounts: catalogCounts(recent, ^uint64(0))}},
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
	if component.Name != "active" || catalogCountValue(component) != ^uint64(0) || !component.LastActiveAt.Equal(recent) {
		t.Fatalf("目录元数据或 uint64 精度不符: %+v", component)
	}
	instances, err := store.Read()
	if err != nil || len(instances) != 2 || len(instances[1].Components) != 2 {
		t.Fatalf("只读查询改写了目录文件: %+v, %v", instances, err)
	}
}

func TestCatalogExpiredHourlyCountsBecomeZeroWithoutRewritingFile(t *testing.T) {
	store := catalog.Store{Root: preparedCatalogRoot(t)}
	now := time.Now().UTC()
	old := now.Add(-4 * time.Hour)
	instance := catalog.Instance{Project: "p", InstanceID: "i", Components: []catalog.Component{{
		Name: "quiet", CreatedAt: old, LastActiveAt: old, LogCounts: catalogCounts(old, 999),
	}}}
	if err := store.Merge(instance, old); err != nil {
		t.Fatal(err)
	}
	path := store.Path("p", "i")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := showCatalog(store, testOptions(t, "--list", "--json"), &output); err != nil {
		t.Fatal(err)
	}
	var selected []catalog.Instance
	if err := json.Unmarshal(output.Bytes(), &selected); err != nil || len(selected) != 1 || len(selected[0].Components) != 1 {
		t.Fatalf("小时数量过期时丢失了仍有效的身份：%s, %v", output.String(), err)
	}
	component := selected[0].Components[0]
	if !component.LastActiveAt.Equal(old) || catalogCountValue(component) != 0 {
		t.Fatalf("小时数量过期后未归零或改写了真实活动：%+v", component)
	}
	lastStart := component.LogCounts[hourly.Hours-1].StartTime
	if !lastStart.Equal(hourly.Start(now)) && !lastStart.Equal(hourly.Start(time.Now())) {
		t.Fatalf("静默组件仍展示过期的小时起点：%+v", component.LogCounts)
	}
	if expected := (hourly.Window{}).Recent(lastStart); component.LogCounts != expected {
		t.Fatalf("查询没有补齐三个带起点的零计数：%+v", component.LogCounts)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("只读查询改写了历史文件：%s, %v", after, err)
	}
}
