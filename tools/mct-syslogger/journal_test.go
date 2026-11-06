package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mctxyz/syslogger/internal/identity"
	"github.com/mctxyz/syslogger/internal/journalwire"
	"github.com/mctxyz/syslogger/internal/logging"
)

// journalTestEntry 为假 journalctl 提供独立的元数据和由 SDK 编码的正文。
type journalTestEntry struct {
	Message   string `json:"MESSAGE"`
	Priority  string `json:"PRIORITY"`
	Project   string `json:"SYSLOGGER_PROJECT"`
	Instance  string `json:"SYSLOGGER_INSTANCE_ID"`
	Component string `json:"SYSLOGGER_COMPONENT"`
	Namespace string `json:"_NAMESPACE"`
}

func journalMatchValues(args []string, prefix string) []string {
	var values []string
	for _, arg := range args {
		if strings.HasPrefix(arg, prefix) {
			values = append(values, strings.TrimPrefix(arg, prefix))
		}
	}
	return values
}

// journalTestLines 模拟同字段 OR、跨字段 AND，以及原生条数限制；反转仍由 TestMain 统一执行。
func journalTestLines(args []string, entries []journalTestEntry) ([]string, error) {
	matches := map[string][]string{}
	for _, argument := range args {
		if strings.HasPrefix(argument, "--") {
			continue
		}
		field, value, match := strings.Cut(argument, "=")
		if match {
			matches[field] = append(matches[field], value)
		}
	}
	var lines []string
	for _, entry := range entries {
		fields := map[string]string{
			"_NAMESPACE":               entry.Namespace,
			"PRIORITY":                 entry.Priority,
			journalwire.ProjectField:   entry.Project,
			journalwire.InstanceField:  entry.Instance,
			journalwire.ComponentField: entry.Component,
		}
		matched := true
		for field, values := range matches {
			if !contains(values, fields[field]) {
				matched = false
				break
			}
		}
		if matched {
			lines = append(lines, entry.Message)
		}
	}
	limit := 0
	for _, value := range journalMatchValues(args, "--lines=") {
		if value == "all" {
			limit = 0
			continue
		}
		var err error
		limit, err = strconv.Atoi(value)
		if err != nil {
			return nil, err
		}
	}
	if contains(args, "--no-tail") {
		limit = 0
	}
	if limit > 0 && len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	return lines, nil
}

func moduleJournalEntry(t *testing.T, message, project, instance, component string, level logging.Level) journalTestEntry {
	t.Helper()
	entry, err := logging.Encode(
		identity.Instance{Project: project, InstanceID: instance}, component, level, message, nil,
		time.Date(2026, 11, 2, 6, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	return journalTestEntry{
		Namespace: journalwire.GroupForSeverity(uint8(level)).Namespace(),
		Message:   string(entry.Body),
		Priority:  strconv.Itoa(int(entry.Severity)),
		Project:   project,
		Instance:  instance,
		Component: component,
	}
}

func fakeJournalEntries(t *testing.T, entries []journalTestEntry) {
	t.Helper()
	fakeJournal(t, "success")
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	// 元数据样本使用隔离文件，避免大型混合日志超过单条环境变量长度限制。
	path := filepath.Join(t.TempDir(), "entries.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SYSLOGGER_LOG_TEST_ENTRIES_FILE", path)
}

func TestJournalNativeMatches(t *testing.T) {
	for _, test := range []struct {
		name       string
		args       []string
		projects   []string
		instances  []string
		components []string
		priorities []string
	}{
		{name: "all-levels"},
		{
			name:       "project-and-threshold",
			args:       []string{"-p", "contract-listener", "-i", "listener-v2", "-c", "rpc", "-l", "notice"},
			projects:   []string{"contract-listener"},
			instances:  []string{"listener-v2"},
			components: []string{"rpc"},
			priorities: []string{"0", "1", "2", "3", "4", "5"},
		},
		{
			name:       "multiple-projects",
			args:       []string{"-p", "first", "-p", "second", "--include-level", "error", "--include-level", "info"},
			projects:   []string{"first", "second"},
			priorities: []string{"3", "6"},
		},
		{
			name:     "raw-project",
			args:     []string{"-p", "a b\t\n\x1b", "-p", "项目\u00a0v2"},
			projects: []string{"a b\t\n\x1b", "项目\u00a0v2"},
		},
		{
			name:     "literal-argument",
			args:     []string{"-p", "a=+; --reverse"},
			projects: []string{"a=+; --reverse"},
		},
		{
			name:       "noncontiguous-levels",
			args:       []string{"-l", "info", "--exclude-level", "error", "--exclude-level", "warning"},
			priorities: []string{"0", "1", "2", "5", "6"},
		},
		{
			name:       "threshold-include-exclude",
			args:       []string{"-l", "warning", "--include-level", "info", "--include-level", "error", "--include-level", "critical", "--exclude-level", "critical"},
			priorities: []string{"3"},
		},
		{
			name:       "empty-levels",
			args:       []string{"--include-level", "error", "--exclude-level", "error"},
			priorities: []string{"8"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := testOptions(t, test.args...).journalArgs()
			if got := journalMatchValues(args, journalwire.ProjectField+"="); !reflect.DeepEqual(got, test.projects) {
				t.Fatalf("项目预筛错误：got=%q want=%q", got, test.projects)
			}
			if got := journalMatchValues(args, journalwire.InstanceField+"="); !reflect.DeepEqual(got, test.instances) {
				t.Fatalf("实例预筛错误：got=%q want=%q", got, test.instances)
			}
			if got := journalMatchValues(args, journalwire.ComponentField+"="); !reflect.DeepEqual(got, test.components) {
				t.Fatalf("组件预筛错误：got=%q want=%q", got, test.components)
			}
			if got := journalMatchValues(args, "PRIORITY="); !reflect.DeepEqual(got, test.priorities) {
				t.Fatalf("等级预筛错误：got=%q want=%q", got, test.priorities)
			}
			if !contains(args, "--lines=all") || !contains(args, "--reverse") {
				t.Fatalf("预筛改变了最近 N 条的读取方式：%q", args)
			}
		})
	}
}

func TestJournalNativeSelectionParity(t *testing.T) {
	var entries []journalTestEntry
	var original strings.Builder
	for _, project := range []string{"first", "second", "a b", "a_b"} {
		for level := logging.Emergency; level <= logging.Debug; level++ {
			entry := moduleJournalEntry(t, project, project, "local-a", "rpc", level)
			entries = append(entries, entry)
			fmt.Fprintln(&original, entry.Message)
		}
	}
	for _, args := range [][]string{
		{},
		{"-l", "notice"},
		{"-p", "first", "-p", "second", "-l", "warning"},
		{"-p", "a b"},
		{"--include-level", "error", "--include-level", "info"},
		{"-l", "info", "--exclude-level", "error", "--exclude-level", "warning"},
		{"-l", "notice", "--include-level", "info"},
		{"--include-level", "error", "--exclude-level", "error"},
	} {
		opts := testOptions(t, append([]string{"-n", "0", "--json"}, args...)...)
		lines, err := journalTestLines(opts.journalArgs(), entries)
		if err != nil {
			t.Fatal(err)
		}
		var expected, output bytes.Buffer
		if _, err := referenceFilterLogs(strings.NewReader(original.String()), &expected, opts); err != nil {
			t.Fatal(err)
		}
		if _, err := filterLogs(strings.NewReader(strings.Join(lines, "\n")), &output, opts, nil); err != nil {
			t.Fatal(err)
		}
		if output.String() != expected.String() {
			t.Fatalf("模块日志预筛前后结果不同：args=%v", args)
		}
	}
}

func TestQueryNativePrefilterBeforeMatchLimit(t *testing.T) {
	var entries []journalTestEntry
	var wanted []string
	for index := 0; index < 3; index++ {
		entry := moduleJournalEntry(t, fmt.Sprintf("needle-%d", index), "contract-listener", "listener-v2", "rpc", logging.Warning)
		entries = append(entries, entry)
		wanted = append(wanted, entry.Message)
	}
	for index := 0; index < 12; index++ {
		entries = append(entries, moduleJournalEntry(t, "needle-other-instance", "contract-listener", "other", "rpc", logging.Warning))
	}
	for index := 0; index < 3; index++ {
		entries = append(entries, moduleJournalEntry(t, "needle-other-component", "contract-listener", "listener-v2", "other", logging.Warning))
		entries = append(entries, moduleJournalEntry(t, "content-miss", "contract-listener", "listener-v2", "rpc", logging.Warning))
	}
	for index := 0; index < 40; index++ {
		entries = append(entries, moduleJournalEntry(t, "needle-info", "contract-listener", "listener-v2", "rpc", logging.Info))
	}
	for index := 0; index < 20; index++ {
		entries = append(entries, moduleJournalEntry(t, "needle-other-project", "other-project", "listener-v2", "rpc", logging.Warning))
	}
	for _, test := range []struct {
		name    string
		args    []string
		want    []string
		count   string
		outcome string
	}{
		{name: "recent", args: []string{"-n", "2"}, want: wanted[1:], count: "scanned_lines=5 ", outcome: "limit"},
		{name: "follow", args: []string{"-f", "-n", "2"}, want: wanted[:2], count: "scanned_lines=2 ", outcome: "limit"},
		{name: "all", args: []string{"-n", "0"}, want: wanted, count: "scanned_lines=6 ", outcome: "eof"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fakeJournalEntries(t, entries)
			args := append([]string{"--debug", "--json", "-p", "contract-listener", "-i", "listener-v2", "-c", "rpc", "-l", "notice", "-k", "needle"}, test.args...)
			var output, diagnostics bytes.Buffer
			if err := query(context.Background(), testOptions(t, args...), &output, &diagnostics); err != nil {
				t.Fatal(err)
			}
			if expected := strings.Join(test.want, "\n") + "\n"; output.String() != expected {
				t.Fatalf("实例、组件、内容筛选或输出顺序错误：%s", output.String())
			}
			if !strings.Contains(diagnostics.String(), test.count) || !strings.Contains(diagnostics.String(), "stop="+test.outcome) {
				t.Fatalf("原生预筛后的读取量或退出条件错误：%s", diagnostics.String())
			}
		})
	}
}

func TestQueryRawProjectIdentity(t *testing.T) {
	wanted := moduleJournalEntry(t, "wanted", "a b", "local-a", "rpc", logging.Error)
	other := moduleJournalEntry(t, "other", "a_b", "local-a", "rpc", logging.Error)
	fakeJournalEntries(t, []journalTestEntry{wanted, other})
	var output bytes.Buffer
	if err := query(context.Background(), testOptions(t, "-p", "a b", "--json", "-n", "0"), &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	if output.String() != wanted.Message+"\n" {
		t.Fatal("原文项目身份索引混入了其他项目")
	}
}

func TestQueryNativeEmptyLevelsKeepsJournalErrors(t *testing.T) {
	fakeJournal(t, "fail")
	var diagnostics bytes.Buffer
	opts := testOptions(t, "--include-level", "error", "--exclude-level", "error")
	if err := query(context.Background(), opts, io.Discard, &diagnostics); err == nil ||
		!strings.Contains(diagnostics.String(), "journal permission denied") {
		t.Fatal("空等级集合仍应保留 journalctl 的执行与错误反馈")
	}
}

func TestIndexedIdentitiesBeforeMatchLimit(t *testing.T) {
	var entries []journalTestEntry
	var wanted []string
	for index := 0; index < 3; index++ {
		entry := moduleJournalEntry(t, fmt.Sprintf("native-%d", index), "p", "local", "rpc", logging.Info)
		entries = append(entries, entry)
		wanted = append(wanted, entry.Message)
	}
	for index := 0; index < 1000; index++ {
		entries = append(entries, moduleJournalEntry(t, "native-other", "p", "other", "rpc", logging.Info))
		entries = append(entries, moduleJournalEntry(t, "native-other-component", "p", "local", "jobs", logging.Info))
	}
	for _, test := range []struct {
		name  string
		args  []string
		want  []string
		count string
	}{
		{name: "recent", args: []string{"-n", "2"}, want: wanted[1:], count: "scanned_lines=2 "},
		{name: "all", args: []string{"-n", "0"}, want: wanted, count: "scanned_lines=3 "},
		{name: "follow", args: []string{"-f", "-n", "2"}, want: wanted[:2], count: "scanned_lines=2 "},
	} {
		t.Run(test.name, func(t *testing.T) {
			fakeJournalEntries(t, entries)
			args := append([]string{"--json", "--debug", "-p", "p", "-i", "local", "-c", "rpc"}, test.args...)
			var output, diagnostics bytes.Buffer
			if err := query(context.Background(), testOptions(t, args...), &output, &diagnostics); err != nil {
				t.Fatal(err)
			}
			if output.String() != strings.Join(test.want, "\n")+"\n" || !strings.Contains(diagnostics.String(), test.count) {
				t.Fatalf("身份索引、顺序或匹配后限量不符: %s\n%s", output.String(), diagnostics.String())
			}
		})
	}
}

func TestIndexedIdentityORAndRawProject(t *testing.T) {
	wanted := moduleJournalEntry(t, "wanted", "a b", "local", "rpc", logging.Error)
	other := moduleJournalEntry(t, "collision", "a_b", "local", "rpc", logging.Error)
	second := moduleJournalEntry(t, "second", "a b", "second", "jobs", logging.Warning)
	entries := []journalTestEntry{wanted, other, second}
	for _, test := range []struct {
		args []string
		want []string
	}{
		{args: nil, want: []string{wanted.Message, other.Message, second.Message}},
		{args: []string{"-i", "local", "-c", "rpc"}, want: []string{wanted.Message, other.Message}},
		{args: []string{"-i", "second"}, want: []string{second.Message}},
		{args: []string{"-c", "jobs"}, want: []string{second.Message}},
		{args: []string{"-p", "a b", "-i", "local", "-c", "rpc"}, want: []string{wanted.Message}},
		{args: []string{"-p", "a b", "-p", "a_b", "-l", "warning"}, want: []string{wanted.Message, other.Message, second.Message}},
		{args: []string{"-p", "a b", "-i", "local", "-i", "second", "-c", "rpc", "-c", "jobs"}, want: []string{wanted.Message, second.Message}},
		{args: []string{"-p", "a b", "-i", "local", "-c", "jobs"}, want: nil},
		{args: []string{"-p", "a b", "-l", "error"}, want: []string{wanted.Message}},
	} {
		opts := testOptions(t, append([]string{"-n", "0", "--json"}, test.args...)...)
		lines, err := journalTestLines(opts.journalArgs(), entries)
		if err != nil || !reflect.DeepEqual(lines, test.want) {
			t.Fatalf("原文身份、OR/AND 或等级索引不符: got=%v want=%v err=%v", lines, test.want, err)
		}
	}
}

func TestIndexedMetadataStillRechecksBody(t *testing.T) {
	wanted := moduleJournalEntry(t, "wanted", "a b", "local", "rpc", logging.Error)
	spoofed := moduleJournalEntry(t, "wrong-body", "a_b", "local", "rpc", logging.Error)
	spoofed.Project = "a b"
	fakeJournalEntries(t, []journalTestEntry{wanted, spoofed})
	var output bytes.Buffer
	if err := query(context.Background(), testOptions(t, "--json", "-n", "0", "-p", "a b", "-i", "local"), &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	if output.String() != wanted.Message+"\n" {
		t.Fatal("原生字段预筛绕过了正文身份复核")
	}
}

func TestJournalGroupSelection(t *testing.T) {
	for _, test := range []struct {
		args []string
		want []string
	}{
		{args: []string{"--include-level", "debug"}, want: []string{"mct-syslogger-debug"}},
		{args: []string{"--include-level", "notice"}, want: []string{"mct-syslogger-info"}},
		{args: []string{"--include-level", "warning"}, want: []string{"mct-syslogger-warning"}},
		{args: []string{"-l", "error"}, want: []string{"mct-syslogger-error"}},
		{args: []string{"-l", "warning"}, want: []string{"mct-syslogger-warning", "mct-syslogger-error"}},
		{args: []string{"--include-level", "debug", "--include-level", "error"}, want: []string{"mct-syslogger-debug", "mct-syslogger-error"}},
	} {
		args := testOptions(t, test.args...).journalArgs()
		if got := journalMatchValues(args, "_NAMESPACE="); !reflect.DeepEqual(got, test.want) {
			t.Fatalf("空间选择错误: args=%v got=%v want=%v", test.args, got, test.want)
		}
	}
	wanted := moduleJournalEntry(t, "wanted", "project", "main", "rpc", logging.Error)
	unrelated := wanted
	unrelated.Namespace = "unrelated"
	defaultJournal := wanted
	defaultJournal.Namespace = ""
	fakeJournalEntries(t, []journalTestEntry{wanted, unrelated, defaultJournal})
	var output bytes.Buffer
	if err := query(context.Background(), testOptions(t, "--json", "-n", "0"), &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	if output.String() != wanted.Message+"\n" {
		t.Fatalf("混入了范围外 namespace: %s", &output)
	}
}
