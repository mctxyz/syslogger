package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func sampleLine(message, instance, component string) string {
	data, err := json.Marshal(map[string]any{
		"timestamp":   "2026-10-23T09:00:00.123456789Z",
		"level":       "error",
		"project":     "syslogger-test",
		"instance_id": instance,
		"component":   component,
		"message":     message,
		"fields": map[string]any{
			"error":   "context deadline exceeded",
			"attempt": 3,
		},
	})
	if err != nil {
		panic(err)
	}
	return string(data)
}

func testOptions(t *testing.T, args ...string) options {
	t.Helper()
	opts, err := parseOptions(args, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	return opts
}

func TestParseOptions(t *testing.T) {
	if opts := testOptions(t); opts.since != "-1h" || opts.lines != 100 || opts.follow || opts.colour {
		t.Fatalf("默认查询选项错误：%+v", opts)
	}
	if opts := testOptions(t, "-f"); opts.lines != 0 {
		t.Fatal("跟踪默认应不限条数")
	}
	if opts := testOptions(t, "--colour"); !opts.colour {
		t.Fatal("显式颜色开关未生效")
	}
	if opts := testOptions(t, "--colour", "--colour=false"); opts.colour {
		t.Fatal("显式关闭颜色未生效")
	}
	for _, args := range [][]string{
		{"-f", "-n", "2"},
		{"--lines", "2", "--follow"},
	} {
		if opts := testOptions(t, args...); opts.lines != 2 || !opts.follow {
			t.Fatalf("显式条数未生效：%+v", opts)
		}
	}
	for _, args := range [][]string{
		{"--regex", "["},
		{"-e", `(?=lookahead)`},
		{"-n", "-1"},
		{"-n", "abc"},
		{"-l", "fatal"},
		{"-f", "--until", "now"},
		{"--unknown"},
		{"--colour=invalid"},
		{"positional"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if _, err := parseOptions(args, io.Discard); err == nil {
				t.Fatal("无效参数未被拒绝")
			}
		})
	}
}

func TestHelpLayout(t *testing.T) {
	var help bytes.Buffer
	if _, err := parseOptions([]string{"--help"}, &help); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("帮助命令失败：%v", err)
	}
	var rows []string
	for _, line := range strings.Split(help.String(), "\n") {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "-") {
			rows = append(rows, line)
		}
	}
	if len(rows) != 14 {
		t.Fatalf("每个功能应只显示一行帮助，实际为 %d 行：\n%s", len(rows), help.String())
	}
	want := []string{
		"-p, --project",
		"-I, --instance-id",
		"-c, --component",
		"-l, --level",
		"-S, --since",
		"-U, --until",
		"-k, --keyword",
		"-e, --regex",
		"-f, --follow",
		"-i, --ignore-case",
		"-n, --lines",
		"--json",
		"--list",
		"--colour",
	}
	for i, label := range want {
		if !strings.HasPrefix(rows[i], label+" ") {
			t.Fatalf("第 %d 行应为 %q，实际为 %q", i+1, label, rows[i])
		}
	}
	for _, row := range rows {
		if strings.Count(row, "--") != 1 {
			t.Fatalf("长短参数未合并为单条说明：%q", row)
		}
	}
	if !strings.Contains(help.String(), "sudo usermod -aG systemd-journal username") {
		t.Fatal("帮助信息缺少普通用户读取 journal 的权限配置示例")
	}
}

func TestJournalArgs(t *testing.T) {
	opts := testOptions(t, "--since=-2h", "--until", "2026-10-23 12:00:00; literal")
	want := []string{
		"--no-pager",
		"--output=cat",
		"--all",
		"--since=-2h",
		"--until=2026-10-23 12:00:00; literal",
		"--lines=all",
		"--reverse",
	}
	if !reflect.DeepEqual(opts.journalArgs(), want) {
		t.Fatalf("命令参数错误：%q", opts.journalArgs())
	}
	for _, args := range [][]string{
		{"-n", "0"},
		{"-f", "--since", "now"},
	} {
		opts := testOptions(t, args...)
		if contains(opts.journalArgs(), "--reverse") || contains(opts.journalArgs(), "--lines=100") {
			t.Fatalf("完整读取被提前截断或反转：%q", opts.journalArgs())
		}
		if opts.follow && !contains(opts.journalArgs(), "--no-tail") {
			t.Fatal("跟踪时应读取起始时间以来的全部历史")
		}
	}
}

func TestParseRecord(t *testing.T) {
	line := sampleLine("", "local-a", "rpc")
	if record, valid := parseRecord([]byte(line)); !valid || record.Message == nil || *record.Message != "" {
		t.Fatal("空消息应作为有效日志读取")
	}
	for _, line := range []string{
		"ordinary syslog message",
		"{broken",
		"null",
		"[]",
		"{}",
		line + " {}",
		strings.Replace(line, `"level":"error"`, `"level":"fatal"`, 1),
		strings.Replace(line, `"message":""`, `"message":null`, 1),
		strings.Replace(line, `"message":""`, `"message":123`, 1),
	} {
		if _, valid := parseRecord([]byte(line)); valid {
			t.Fatalf("普通消息或无效日志未跳过：%s", line)
		}
	}
}

func TestFilterComposition(t *testing.T) {
	record, _ := parseRecord([]byte(sampleLine("RPC [timeout]", "local-a", "worker")))
	for _, test := range []struct {
		args []string
		want bool
	}{
		{
			args: []string{
				"-p", "syslogger-test",
				"-I", "local-a",
				"-c", "rpc",
				"-c", "worker",
				"-l", "error",
				"-l", "critical",
			},
			want: true,
		},
		{
			args: []string{"-I", "other"},
			want: false,
		},
		{
			args: []string{"-c", "rpc"},
			want: false,
		},
		{
			args: []string{"-l", "info"},
			want: false,
		},
		{
			args: []string{
				"-k", "[timeout]",
				"-k", "deadline",
				"-k", "attempt",
			},
			want: true,
		},
		{
			args: []string{
				"-k", "timeout",
				"-k", "missing",
			},
			want: false,
		},
		{
			args: []string{"-k", "syslogger-test"},
			want: false,
		},
		{
			args: []string{"-k", "rpc"},
			want: false,
		},
		{
			args: []string{"-k", "rpc", "--ignore-case"},
			want: true,
		},
		{
			args: []string{"-k", "DEADLINE", "-i"},
			want: true,
		},
		{
			args: []string{
				"-k", "rpc",
				"-e", `^rpc \[timeout\]$`,
				"-i",
			},
			want: false,
		},
		{
			args: []string{
				"-k", "rpc",
				"-e", `(?i)^rpc \[timeout\]$`,
				"-i",
			},
			want: true,
		},
		{
			args: []string{"-e", `(?i)^rpc \[timeout\]$`},
			want: true,
		},
		{
			args: []string{"-e", `DEADLINE`, "-i"},
			want: false,
		},
		{
			args: []string{"-e", `(?i)DEADLINE`},
			want: true,
		},
		{
			args: []string{"-I", "LOCAL-A", "-i"},
			want: false,
		},
		{
			args: []string{
				"-e", `timeout|refused`,
				"-e", `"attempt":3`,
			},
			want: true,
		},
		{
			args: []string{
				"-e", `timeout`,
				"-e", `missing`,
			},
			want: false,
		},
	} {
		t.Run(strings.Join(test.args, " "), func(t *testing.T) {
			if got := testOptions(t, test.args...).matches(record); got != test.want {
				t.Fatalf("过滤结果为 %v，期望 %v", got, test.want)
			}
		})
	}
}

func TestFieldsAndOutputPreserveContent(t *testing.T) {
	line := sampleLine("消息\nnext\t\x1b[31m", "local-a", "rpc")
	line = strings.Replace(line, `"attempt":3`, `"attempt":9007199254740993,"nested":{"detail":"请求\u0026失败"}`, 1)
	record, valid := parseRecord([]byte(line))
	if !valid || !testOptions(t, "-k", "9007199254740993", "-k", "请求&失败").matches(record) {
		t.Fatal("业务字段解码丢失数字精度或转义内容")
	}
	if got := record.format(true, false); got != line {
		t.Fatalf("JSON 输出改变了正文：%s", got)
	}
	text := record.format(false, false)
	if strings.ContainsAny(text, "\n\t\x1b") || !strings.Contains(text, "9007199254740993") || !strings.Contains(text, "消息") {
		t.Fatalf("可读输出未保持单行及字段内容：%q", text)
	}
}

func TestColourFormatting(t *testing.T) {
	ansi := regexp.MustCompile(`\x1b\[[0-9;]+m`)
	seenLevelStyles := make(map[string]bool)
	for _, level := range levels {
		t.Run(level, func(t *testing.T) {
			line := strings.Replace(
				sampleLine("消息\nnext\t\x1b[31m", "local-a", "rpc"),
				`"level":"error"`,
				`"level":`+strconv.Quote(level),
				1,
			)
			record, valid := parseRecord([]byte(line))
			if !valid {
				t.Fatal("日志解析失败")
			}
			style := levelColours[level]
			if style == "" || seenLevelStyles[style] {
				t.Fatalf("等级 %s 缺少独立颜色样式", level)
			}
			seenLevelStyles[style] = true
			plain := record.format(false, false)
			coloured := record.format(false, true)
			if ansi.MatchString(plain) {
				t.Fatal("默认输出中出现 ANSI 颜色")
			}
			if got := ansi.ReplaceAllString(coloured, ""); got != plain {
				t.Fatalf("着色改变了文本、转义或列宽：\nplain=%q\ncoloured=%q", plain, got)
			}
			levelText := fmt.Sprintf("%-9s", strings.ToUpper(level))
			if !strings.Contains(coloured, style+levelText+colourReset) {
				t.Fatal("等级样式未完整应用或复位")
			}
			seenFieldStyles := make(map[string]bool)
			for _, field := range []struct {
				style string
				text  string
			}{
				{
					style: colourProject,
					text:  record.Project,
				},
				{
					style: colourInstance,
					text:  record.InstanceID,
				},
				{
					style: colourComponent,
					text:  record.Component,
				},
				{
					style: colourMessage,
					text:  escaped(*record.Message),
				},
				{
					style: colourFields,
					text:  record.fieldsJSON(),
				},
			} {
				if field.style == "" || seenFieldStyles[field.style] {
					t.Fatal("日志字段应使用不同的颜色")
				}
				seenFieldStyles[field.style] = true
				if !strings.Contains(coloured, field.style+field.text+colourReset) {
					t.Fatalf("字段 %q 未独立着色或复位", field.text)
				}
			}
			if got := record.format(true, true); got != line {
				t.Fatalf("颜色开关改变了 JSON 正文：%s", got)
			}
		})
	}
}

func TestColourWithoutFields(t *testing.T) {
	record, valid := parseRecord([]byte(sampleLine("", "local-a", "rpc")))
	if !valid {
		t.Fatal("日志解析失败")
	}
	record.Fields = nil
	coloured := record.format(false, true)
	if strings.Contains(coloured, colourFields) {
		t.Fatal("空业务字段出现了颜色片段")
	}
	ansi := regexp.MustCompile(`\x1b\[[0-9;]+m`)
	if got := ansi.ReplaceAllString(coloured, ""); got != record.format(false, false) {
		t.Fatal("空消息或空业务字段着色改变了文本布局")
	}
}

func TestFilterLogsColour(t *testing.T) {
	line := sampleLine("message", "local-a", "rpc")
	for _, test := range []struct {
		name       string
		args       []string
		wantColour bool
	}{
		{
			name:       "plain",
			wantColour: false,
		},
		{
			name:       "recent",
			args:       []string{"--colour"},
			wantColour: true,
		},
		{
			name:       "all",
			args:       []string{"--colour", "-n", "0"},
			wantColour: true,
		},
		{
			name:       "follow",
			args:       []string{"--colour", "-f"},
			wantColour: true,
		},
		{
			name:       "disabled",
			args:       []string{"--colour", "--colour=false"},
			wantColour: false,
		},
		{
			name:       "json",
			args:       []string{"--colour", "--json"},
			wantColour: false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			opts := testOptions(t, test.args...)
			var output bytes.Buffer
			complete, err := filterLogs(strings.NewReader(line+"\n"), &output, opts)
			if err != nil || !complete {
				t.Fatalf("颜色输出读取失败：complete=%v err=%v", complete, err)
			}
			if coloured := strings.Contains(output.String(), "\x1b["); coloured != test.wantColour {
				t.Fatalf("颜色开关未按模式生效：%q", output.String())
			}
			if opts.asJSON && output.String() != line+"\n" {
				t.Fatal("JSON 输出未保留原始正文")
			}
		})
	}
}

func TestReadableTimestamp(t *testing.T) {
	for _, test := range []struct {
		timestamp string
		want      string
	}{
		{
			timestamp: "2026-10-23T09:00:00.987654321Z",
			want:      "2026-10-23 09:00:00",
		},
		{
			timestamp: "2026-10-23T09:00:00Z",
			want:      "2026-10-23 09:00:00",
		},
		{
			timestamp: "2026-10-23T01:00:00.123+08:00",
			want:      "2026-10-22 17:00:00",
		},
		{
			timestamp: "invalid\ntime",
			want:      `invalid\ntime`,
		},
	} {
		t.Run(test.timestamp, func(t *testing.T) {
			line := strings.Replace(
				sampleLine("message", "local-a", "rpc"),
				`"timestamp":"2026-10-23T09:00:00.123456789Z"`,
				`"timestamp":`+strconv.Quote(test.timestamp),
				1,
			)
			record, valid := parseRecord([]byte(line))
			if !valid {
				t.Fatal("日志解析失败")
			}
			if got := record.format(false, false); !strings.HasPrefix(got, test.want+" ERROR") {
				t.Fatalf("可读时间格式错误：%q", got)
			}
			if got := record.format(true, false); got != line {
				t.Fatalf("JSON 输出改变了原始时间：%s", got)
			}
		})
	}
}

func TestMatchLimitAndOrder(t *testing.T) {
	var input strings.Builder
	for i := 259; i >= 0; i-- {
		instance := "other"
		if i%2 == 0 {
			instance = "local-a"
		}
		fmt.Fprintln(&input, sampleLine(fmt.Sprint(i), instance, "rpc"))
	}
	var output bytes.Buffer
	complete, err := filterLogs(strings.NewReader(input.String()), &output, testOptions(t, "-I", "local-a", "--json"))
	if err != nil || complete {
		t.Fatalf("达到匹配数量应提前停止：complete=%v err=%v", complete, err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 100 {
		t.Fatalf("应在过滤后限制条数，得到 %d 条", len(lines))
	}
	for i, line := range lines {
		record, valid := parseRecord([]byte(line))
		if !valid || *record.Message != fmt.Sprint(60+i*2) {
			t.Fatalf("第 %d 条顺序错误：%s", i, line)
		}
	}
}

func TestStreamAllAndFollow(t *testing.T) {
	var input strings.Builder
	for i := 0; i < 150; i++ {
		fmt.Fprintln(&input, sampleLine(fmt.Sprint(i), "local-a", "rpc"))
	}
	for _, args := range [][]string{
		{"-n", "0"},
		{"-f"},
	} {
		var output bytes.Buffer
		complete, err := filterLogs(strings.NewReader(input.String()), &output, testOptions(t, append(args, "--json")...))
		if err != nil || !complete || output.String() != input.String() {
			t.Fatalf("全量读取未保持顺序：args=%v complete=%v err=%v", args, complete, err)
		}
	}
	var output bytes.Buffer
	complete, err := filterLogs(strings.NewReader(input.String()), &output, testOptions(t, "-f", "-n", "2", "--json"))
	if err != nil || complete || strings.Count(output.String(), "\n") != 2 {
		t.Fatalf("跟踪模式未按显式条数退出：complete=%v err=%v", complete, err)
	}
}

func TestLargeAndFinalLines(t *testing.T) {
	line := sampleLine(strings.Repeat("消息", 30000), "local-a", "rpc")
	var output bytes.Buffer
	complete, err := filterLogs(strings.NewReader("ordinary log\n{}\n"+line), &output, testOptions(t, "-n", "0", "--json"))
	if err != nil || !complete || output.String() != line+"\n" {
		t.Fatalf("大日志或末行读取失败：complete=%v err=%v", complete, err)
	}
}

// TestMain 在子进程中模拟 journalctl，测试路径只包含该辅助进程。
func TestMain(m *testing.M) {
	if os.Getenv("SYSLOGGER_LOG_TEST_HELPER") == "1" {
		if os.Getenv("SYSLOGGER_LOG_TEST_MODE") == "fail" {
			fmt.Fprintln(os.Stderr, "journal permission denied")
			os.Exit(7)
		}
		lines := strings.Split(strings.TrimSuffix(os.Getenv("SYSLOGGER_LOG_TEST_DATA"), "\n"), "\n")
		if contains(os.Args[1:], "--reverse") {
			for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
				lines[i], lines[j] = lines[j], lines[i]
			}
		}
		for _, line := range lines {
			fmt.Fprintln(os.Stdout, line)
		}
		if os.Getenv("SYSLOGGER_LOG_TEST_MODE") == "hold" {
			<-time.NewTimer(time.Minute).C
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeJournal(t *testing.T, mode string) {
	t.Helper()
	directory := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(executable, filepath.Join(directory, "journalctl")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	t.Setenv("SYSLOGGER_LOG_TEST_HELPER", "1")
	t.Setenv("SYSLOGGER_LOG_TEST_MODE", mode)
	t.Setenv("SYSLOGGER_LOG_TEST_DATA", sampleLine("wanted", "local-a", "rpc"))
}

func TestQueryNaturalExitAndFailure(t *testing.T) {
	for _, mode := range []string{
		"success",
		"fail",
	} {
		t.Run(mode, func(t *testing.T) {
			fakeJournal(t, mode)
			var output bytes.Buffer
			var diagnostics bytes.Buffer
			err := query(context.Background(), testOptions(t), &output, &diagnostics)
			if mode == "success" && (err != nil || output.Len() == 0) {
				t.Fatalf("正常查询失败：%v", err)
			}
			if mode == "fail" && (err == nil || !strings.Contains(diagnostics.String(), "permission denied")) {
				t.Fatalf("子进程错误未反馈：err=%v diagnostics=%q", err, diagnostics.String())
			}
		})
	}
}

type callbackWriter struct {
	write func() error
}

func (writer callbackWriter) Write(data []byte) (int, error) {
	if err := writer.write(); err != nil {
		return 0, err
	}
	return len(data), nil
}

func TestQueryEarlyStopCleanup(t *testing.T) {
	for _, mode := range []string{
		"limit",
		"output-error",
		"cancel",
	} {
		t.Run(mode, func(t *testing.T) {
			fakeJournal(t, "hold")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			opts := testOptions(t, "-f", "-n", "1")
			outputError := errors.New("output closed")
			var output io.Writer = io.Discard
			if mode == "output-error" {
				output = callbackWriter{
					write: func() error {
						return outputError
					},
				}
			}
			if mode == "cancel" {
				opts.lines = 0
				output = callbackWriter{
					write: func() error {
						cancel()
						return nil
					},
				}
			}
			err := query(ctx, opts, output, io.Discard)
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				t.Fatal("提前停止后仍等待日志进程，直到测试超时")
			}
			switch mode {
			case "limit":
				if err != nil {
					t.Fatal(err)
				}
			case "output-error":
				if !errors.Is(err, outputError) {
					t.Fatalf("输出错误未反馈：%v", err)
				}
			case "cancel":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("取消状态未反馈：%v", err)
				}
			}
		})
	}
}

func TestQueryMissingJournal(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if err := query(context.Background(), testOptions(t), io.Discard, io.Discard); err == nil {
		t.Fatal("缺少 journalctl 时应反馈错误")
	}
}
