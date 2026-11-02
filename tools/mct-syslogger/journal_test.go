package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mctxyz/syslogger/internal/identity"
	"github.com/mctxyz/syslogger/internal/logging"
)

// journalTestEntry 为假 journalctl 提供独立的元数据和由 SDK 编码的正文。
type journalTestEntry struct {
	Message    string `json:"MESSAGE"`
	Identifier string `json:"SYSLOG_IDENTIFIER"`
	Priority   string `json:"PRIORITY"`
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
	identifiers := journalMatchValues(args, "SYSLOG_IDENTIFIER=")
	priorities := journalMatchValues(args, "PRIORITY=")
	var lines []string
	for _, entry := range entries {
		if len(identifiers) > 0 && !contains(identifiers, entry.Identifier) {
			continue
		}
		if len(priorities) > 0 && !contains(priorities, entry.Priority) {
			continue
		}
		lines = append(lines, entry.Message)
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
		Message:    string(entry.Body),
		Identifier: identity.SyslogTag(project),
		Priority:   strconv.Itoa(int(entry.Severity)),
	}
}

func fakeJournalEntries(t *testing.T, entries []journalTestEntry) {
	t.Helper()
	fakeJournal(t, "success")
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SYSLOGGER_LOG_TEST_ENTRIES", string(data))
}

func TestJournalNativeMatches(t *testing.T) {
	for _, test := range []struct {
		name        string
		args        []string
		identifiers []string
		priorities  []string
	}{
		{name: "all-levels"},
		{
			name:        "project-and-threshold",
			args:        []string{"-p", "contract-listener", "-i", "listener-v2", "-c", "rpc", "-l", "notice"},
			identifiers: []string{"contract-listener"},
			priorities:  []string{"0", "1", "2", "3", "4", "5"},
		},
		{
			name:        "multiple-projects",
			args:        []string{"-p", "first", "-p", "second", "--include-level", "error", "--include-level", "info"},
			identifiers: []string{"first", "second"},
			priorities:  []string{"3", "6"},
		},
		{
			name:        "normalized-project",
			args:        []string{"-p", "a b\t\n\x1b", "-p", "项目\u00a0v2"},
			identifiers: []string{"a_b___", "项目_v2"},
		},
		{
			name:        "literal-argument",
			args:        []string{"-p", "a=+; --reverse"},
			identifiers: []string{"a=+;_--reverse"},
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
			if got := journalMatchValues(args, "SYSLOG_IDENTIFIER="); !reflect.DeepEqual(got, test.identifiers) {
				t.Fatalf("项目预筛错误：got=%q want=%q", got, test.identifiers)
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
		{name: "recent", args: []string{"-n", "2"}, want: wanted[1:], count: "scanned_lines=20 ", outcome: "limit"},
		{name: "follow", args: []string{"-f", "-n", "2"}, want: wanted[:2], count: "scanned_lines=2 ", outcome: "limit"},
		{name: "all", args: []string{"-n", "0"}, want: wanted, count: "scanned_lines=21 ", outcome: "eof"},
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

func TestQueryNativeTagCollision(t *testing.T) {
	wanted := moduleJournalEntry(t, "wanted", "a b", "local-a", "rpc", logging.Error)
	other := moduleJournalEntry(t, "other", "a_b", "local-a", "rpc", logging.Error)
	fakeJournalEntries(t, []journalTestEntry{wanted, other})
	var output bytes.Buffer
	if err := query(context.Background(), testOptions(t, "-p", "a b", "--json", "-n", "0"), &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	if output.String() != wanted.Message+"\n" {
		t.Fatal("tag 归一化碰撞应由正文项目精确匹配消解")
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
