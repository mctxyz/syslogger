package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mctxyz/syslogger/internal/journalwire"
)

type rangeFixture struct {
	First string
	Last  string
	Mode  string
}

func rangeFixtures() map[string]rangeFixture {
	base := time.Date(2026, 11, 6, 10, 0, 0, 123456000, time.UTC)
	fixtures := make(map[string]rangeFixture)
	for group := journalwire.DebugGroup; group < journalwire.GroupCount; group++ {
		fixtures[group.Namespace()] = rangeFixture{
			First: strconv.FormatInt(base.Add(-time.Duration(group+1)*time.Hour).UnixMicro(), 10),
			Last:  strconv.FormatInt(base.Add(time.Duration(group)*time.Second).UnixMicro(), 10),
		}
	}
	fixtures[journalwire.WarningGroup.Namespace()] = rangeFixture{}
	errorSpace := fixtures[journalwire.ErrorGroup.Namespace()]
	errorSpace.First = errorSpace.Last
	fixtures[journalwire.ErrorGroup.Namespace()] = errorSpace
	return fixtures
}

func fakeRanges(t *testing.T, fixtures map[string]rangeFixture) {
	t.Helper()
	fakeJournal(t, "success")
	data, err := json.Marshal(fixtures)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ranges.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SYSLOGGER_RANGES_TEST_HELPER", "1")
	t.Setenv("SYSLOGGER_RANGES_TEST_FILE", path)
}

// rangeJournalHelper 在独立进程中验证参数及流式早停，始终读取测试夹具。
func rangeJournalHelper() int {
	data, err := os.ReadFile(os.Getenv("SYSLOGGER_RANGES_TEST_FILE"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	var fixtures map[string]rangeFixture
	if err := json.Unmarshal(data, &fixtures); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	args := os.Args[1:]
	namespaces := journalMatchValues(args, "--namespace=")
	if len(namespaces) != 1 {
		fmt.Fprintln(os.Stderr, "expected one namespace")
		return 2
	}
	fixture, exists := fixtures[namespaces[0]]
	if !exists {
		fmt.Fprintln(os.Stderr, "unexpected namespace", namespaces[0])
		return 2
	}
	want := []string{
		"--no-pager", "--namespace=" + namespaces[0],
		"--output=json", "--output-fields=__REALTIME_TIMESTAMP",
	}
	latest := contains(args, "--reverse")
	if latest {
		want = append(want, "--reverse", "--lines=1")
	} else {
		want = append(want, "--lines=all")
	}
	if !reflect.DeepEqual(args, want) {
		fmt.Fprintf(os.Stderr, "unexpected range arguments: %q\n", args)
		return 2
	}
	switch fixture.Mode {
	case "fail":
		fmt.Fprintln(os.Stderr, "journal permission denied")
		return 7
	case "bad-json":
		fmt.Fprintln(os.Stdout, "{broken")
		return 0
	case "wait":
		<-time.NewTimer(time.Minute).C
		return 0
	}
	stamp := fixture.First
	if latest {
		stamp = fixture.Last
	}
	if stamp != "" {
		if err := json.NewEncoder(os.Stdout).Encode(map[string]string{
			"__REALTIME_TIMESTAMP": stamp,
			"__CURSOR":             "fixture-cursor",
			"_BOOT_ID":             "fixture-boot",
		}); err != nil {
			return 1
		}
	}
	if fixture.Mode == "hold-first" && !latest {
		<-time.NewTimer(time.Minute).C
	}
	if fixture.Mode == "fail-last" && latest {
		fmt.Fprintln(os.Stderr, "journal read failed after metadata")
		return 7
	}
	return 0
}

func TestRangeOptions(t *testing.T) {
	if opts := testOptions(t, "--ranges", "--json"); !opts.ranges || !opts.asJSON {
		t.Fatal("空间范围模式或 JSON 参数未生效")
	}
	if opts := testOptions(t, "--ranges=false"); opts.ranges {
		t.Fatal("显式关闭空间范围模式未生效")
	}
	for _, extra := range [][]string{
		{"--list"}, {"-p", "example-project"}, {"-i", "example-instance"}, {"-c", "example-component"},
		{"-l", "debug"}, {"--include-level", "info"}, {"--exclude-level", "debug"},
		{"-s=-1h"}, {"-u", "now"}, {"-n", "1"}, {"-f"},
		{"-k", "message"}, {"-e", "message"}, {"--ignore-case"}, {"--debug"}, {"--colour"},
	} {
		args := append([]string{"--ranges"}, extra...)
		if _, err := parseOptions(args, io.Discard); err == nil {
			t.Fatalf("空间范围模式静默忽略了过滤参数：%v", args)
		}
	}
}

func TestRangesJSONAndLocalText(t *testing.T) {
	fixtures := rangeFixtures()
	fakeRanges(t, fixtures)
	original := time.Local
	time.Local = time.FixedZone("local", 5*60*60+45*60)
	t.Cleanup(func() { time.Local = original })
	var output bytes.Buffer
	if err := showRanges(context.Background(), true, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	var got []journalRange
	if err := json.Unmarshal(output.Bytes(), &got); err != nil || len(got) != int(journalwire.GroupCount) {
		t.Fatalf("JSON 空间概览不符：%s, %v", &output, err)
	}
	for group := journalwire.DebugGroup; group < journalwire.GroupCount; group++ {
		current := got[group]
		fixture := fixtures[group.Namespace()]
		if current.Namespace != group.Namespace() {
			t.Fatalf("空间顺序或名称不符：%+v", got)
		}
		if fixture.First == "" {
			if current.FirstEntry != nil || current.LastEntry != nil {
				t.Fatalf("空空间没有使用 null 端点：%+v", current)
			}
			continue
		}
		first, _ := strconv.ParseInt(fixture.First, 10, 64)
		last, _ := strconv.ParseInt(fixture.Last, 10, 64)
		if current.FirstEntry == nil || current.LastEntry == nil {
			t.Fatalf("有记录的空间缺少时间边界：%+v", current)
		}
		if current.FirstEntry.UnixMicro() != first || current.LastEntry.UnixMicro() != last {
			t.Fatalf("首尾边界或微秒精度不符：%+v", current)
		}
	}
	if strings.Contains(output.String(), "+05:45") || !strings.Contains(output.String(), ".123456Z") {
		t.Fatalf("JSON 未保持 UTC 和微秒精度：%s", &output)
	}
	output.Reset()
	if err := showRanges(context.Background(), false, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	rows := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(rows) != 5 || !strings.Contains(rows[0], "FIRST_ENTRY") || !strings.Contains(rows[0], "LAST_ENTRY") {
		t.Fatalf("概览表头或空间数量不符：%s", &output)
	}
	if !strings.Contains(rows[1], "2026-11-06 14:45:00 +0545") || !strings.Contains(rows[1], "2026-11-06 15:45:00 +0545") {
		t.Fatalf("文字边界未使用本地时区及偏移：%s", &output)
	}
	if got := strings.Fields(rows[3]); !reflect.DeepEqual(got, []string{journalwire.WarningGroup.Namespace(), "-", "-"}) {
		t.Fatalf("空空间没有明确显示缺少边界：%q", rows[3])
	}
}

func TestRangeBoundaryStopsAfterFirstMetadata(t *testing.T) {
	fixtures := rangeFixtures()
	fixture := fixtures[journalwire.DebugGroup.Namespace()]
	fixture.Mode = "hold-first"
	fixtures[journalwire.DebugGroup.Namespace()] = fixture
	fakeRanges(t, fixtures)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if got, err := journalBoundary(ctx, journalwire.DebugGroup.Namespace(), false, io.Discard); err != nil || got == nil {
		t.Fatalf("读取首条元数据后没有及时收尾：%v, %v", got, err)
	}
}

func TestRangesErrorsAndCancellation(t *testing.T) {
	for _, test := range []struct {
		name    string
		fixture rangeFixture
		message string
	}{
		{name: "permission", fixture: rangeFixture{Mode: "fail"}, message: "最早记录"},
		{name: "bad-json", fixture: rangeFixture{Mode: "bad-json"}, message: "元数据"},
		{name: "bad-timestamp", fixture: rangeFixture{First: "invalid"}, message: "时间戳"},
		{name: "latest-exit", fixture: rangeFixture{First: "1000000", Last: "2000000", Mode: "fail-last"}, message: "最新记录"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixtures := rangeFixtures()
			fixtures[journalwire.DebugGroup.Namespace()] = test.fixture
			fakeRanges(t, fixtures)
			var output, diagnostics bytes.Buffer
			err := showRanges(context.Background(), true, &output, &diagnostics)
			if err == nil {
				t.Fatal("查询错误被吞掉")
			}
			if !strings.Contains(err.Error(), test.message) || !strings.Contains(err.Error(), journalwire.DebugGroup.Namespace()) {
				t.Fatalf("查询错误缺少空间或阶段上下文：%v", err)
			}
			if output.Len() != 0 {
				t.Fatalf("查询失败时输出了部分概览：%q", &output)
			}
			if test.name == "permission" && !strings.Contains(diagnostics.String(), "permission denied") {
				t.Fatal("journalctl 的权限诊断被隐藏")
			}
		})
	}
	t.Run("missing-journalctl", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		if err := showRanges(context.Background(), false, io.Discard, io.Discard); err == nil {
			t.Fatal("缺少 journalctl 没有明确报错")
		}
	})
	t.Run("canceled", func(t *testing.T) {
		fixtures := rangeFixtures()
		fixtures[journalwire.DebugGroup.Namespace()] = rangeFixture{Mode: "wait"}
		fakeRanges(t, fixtures)
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		if err := showRanges(ctx, true, io.Discard, io.Discard); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("取消没有传回调用方：%v", err)
		}
	})
}

func TestRangesConcurrentCleanupAndOutputFailure(t *testing.T) {
	fixtures := rangeFixtures()
	fixtures[journalwire.DebugGroup.Namespace()] = rangeFixture{First: "1000000"}
	fakeRanges(t, fixtures)
	var output bytes.Buffer
	if err := showRanges(context.Background(), true, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	var got []journalRange
	if err := json.Unmarshal(output.Bytes(), &got); err != nil || len(got) != int(journalwire.GroupCount) {
		t.Fatalf("清理期间的空间 JSON 不符：%s, %v", &output, err)
	}
	if got[0].FirstEntry != nil || got[0].LastEntry != nil {
		t.Fatalf("两次查询之间清空空间后未保持空端点一致：%s", &output)
	}
	writer := &callbackWriter{write: func() error { return syscall.EPIPE }}
	if err := showRanges(context.Background(), false, writer, io.Discard); !errors.Is(err, syscall.EPIPE) {
		t.Fatalf("输出错误未传回主入口：%v", err)
	}
}

func TestRangesMainDispatch(t *testing.T) {
	fakeRanges(t, rangeFixtures())
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "--ranges", "--json")
	var output, diagnostics bytes.Buffer
	command.Stdout = &output
	command.Stderr = &diagnostics
	if err := command.Run(); err != nil || diagnostics.Len() != 0 {
		t.Fatalf("空间范围主入口失败：%v %s", err, &diagnostics)
	}
	var got []journalRange
	if err := json.Unmarshal(output.Bytes(), &got); err != nil || len(got) != int(journalwire.GroupCount) {
		t.Fatalf("空间范围主入口没有返回完整 JSON：%s, %v", &output, err)
	}
}
