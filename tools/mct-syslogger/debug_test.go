package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

func TestDebugOptions(t *testing.T) {
	if opts := testOptions(t, "-l", "debug"); opts.debug {
		t.Fatal("等级阈值 debug 不应开启性能诊断")
	}
	if opts := testOptions(t, "--debug", "-l", "notice"); !opts.debug || contains(opts.levels, "info") {
		t.Fatal("性能诊断与日志等级筛选应独立生效")
	}
	if opts := testOptions(t, "--debug", "--debug=false"); opts.debug {
		t.Fatal("显式关闭性能诊断未生效")
	}
	if _, err := parseOptions([]string{"--debug=invalid"}, io.Discard); err == nil {
		t.Fatal("无效布尔值应被拒绝")
	}
}

func TestDebugFilterCountsAndFields(t *testing.T) {
	input := strings.Join([]string{
		"ordinary journal message",
		"{}",
		sampleLine("wanted", "other", "rpc"),
		strings.Replace(sampleLine("wanted", "local-a", "rpc"), `"level":"error"`, `"level":"debug"`, 1),
		sampleLine("absent", "local-a", "rpc"),
		sampleLine("wanted", "local-a", "rpc"),
		strings.Replace(sampleLine("field", "local-a", "rpc"), "context deadline exceeded", "wanted", 1),
	}, "\n")
	for _, asJSON := range []bool{false, true} {
		t.Run(fmt.Sprint(asJSON), func(t *testing.T) {
			opts := testOptions(t, "-p", "syslogger-test", "-i", "local-a", "-l", "notice", "-k", "wanted", "-n", "0")
			opts.asJSON = asJSON
			var expected, output bytes.Buffer
			_, err := filterLogs(strings.NewReader(input), &expected, opts, nil)
			if err != nil {
				t.Fatal(err)
			}
			stats := &queryStats{started: time.Now()}
			complete, err := filterLogs(strings.NewReader(input), &output, opts, stats)
			if err != nil || !complete || output.String() != expected.String() {
				t.Fatalf("开启统计改变了输出：complete=%v err=%v", complete, err)
			}
			if stats.scannedLines != 7 || stats.scannedBytes != int64(len(input)) || stats.validRecords != 5 ||
				stats.baseMatches != 3 || stats.matched != 2 || stats.written != 2 || stats.stopReason != "eof" {
				t.Fatalf("扫描和分级筛选计数错误：%+v", stats)
			}
			wantFields := int64(3)
			if asJSON {
				wantFields = 2
			}
			if stats.fieldsDecodes != wantFields || stats.fieldsEncodes != wantFields {
				t.Fatalf("fields 未按需展开并复用：decodes=%d encodes=%d", stats.fieldsDecodes, stats.fieldsEncodes)
			}
			if stats.filterTime+stats.formatTime < stats.fieldsDecode+stats.fieldsEncode {
				t.Fatal("fields 子阶段耗时应包含在筛选和格式化中")
			}
			if stats.firstLine > stats.firstMatch || stats.firstMatch > stats.firstOutput {
				t.Fatal("首行、首匹配和首输出的先后关系错误")
			}
		})
	}
}

func TestQueryDebugSummary(t *testing.T) {
	for _, test := range []struct {
		name    string
		mode    string
		args    []string
		outcome string
		wantErr bool
		wantLog bool
	}{
		{name: "eof", mode: "success", args: []string{"--debug", "-n", "0"}, outcome: "eof", wantLog: true},
		{name: "limit", mode: "hold", args: []string{"--debug", "-f", "-n", "1"}, outcome: "limit", wantLog: true},
		{name: "no-match", mode: "success", args: []string{"--debug", "-p", "missing"}, outcome: "eof"},
		{name: "journal-error", mode: "fail", args: []string{"--debug"}, outcome: "error", wantErr: true},
		{name: "disabled", mode: "success", args: []string{"--debug=false"}, wantLog: true},
		{name: "level-only", mode: "success", args: []string{"-l", "debug"}, wantLog: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fakeJournal(t, test.mode)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var output, diagnostics bytes.Buffer
			opts := testOptions(t, append([]string{"--json"}, test.args...)...)
			err := query(ctx, opts, &output, &diagnostics)
			if (err != nil) != test.wantErr || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				t.Fatalf("查询错误或收尾行为改变：%v", err)
			}
			want := ""
			if test.wantLog {
				want = sampleLine("wanted", "local-a", "rpc") + "\n"
			}
			if output.String() != want {
				t.Fatalf("性能统计污染了 JSON 输出：%q", output.String())
			}
			if !opts.debug {
				if diagnostics.Len() != 0 {
					t.Fatalf("未开启诊断时出现统计：%q", diagnostics.String())
				}
				return
			}
			for _, key := range []string{
				"[debug] query:", "stop=" + test.outcome,
				"journalctl: args=", "start=", "read=", "parse=", "filter=", "format=", "write=", "wait=", "other=",
				"scanned_lines=", "scanned_bytes=", "valid=", "base_matches=", "matched=", "written=",
				"decodes=0 encodes=0", "included in filter/format",
			} {
				if !strings.Contains(diagnostics.String(), key) {
					t.Fatalf("缺少诊断字段 %q：%s", key, diagnostics.String())
				}
			}
			if strings.Count(diagnostics.String(), "[debug] query:") != 1 || strings.Contains(diagnostics.String(), `"--debug"`) {
				t.Fatal("统计应只汇总一次，且 debug 参数不应传给 journalctl")
			}
			if test.name == "no-match" && !strings.Contains(diagnostics.String(), "match=n/a output=n/a") {
				t.Fatal("无匹配时不应伪造首匹配或首输出时间")
			}
			if test.mode == "fail" && !strings.Contains(diagnostics.String(), "journal permission denied") {
				t.Fatal("原有 journalctl 错误诊断应保留")
			}
		})
	}
}

func TestDebugRecentLimitAndOrder(t *testing.T) {
	var input strings.Builder
	for index := 259; index >= 0; index-- {
		instance := "other"
		if index%2 == 0 {
			instance = "local-a"
		}
		fmt.Fprintln(&input, sampleLine(fmt.Sprint(index), instance, "rpc"))
	}
	for _, args := range [][]string{{}, {"-n", "5"}, {"-f", "-n", "5"}} {
		opts := testOptions(t, append([]string{"--json", "-i", "local-a"}, args...)...)
		stats := &queryStats{started: time.Now()}
		var output, expected bytes.Buffer
		_, err := referenceFilterLogs(strings.NewReader(input.String()), &expected, opts)
		if err != nil {
			t.Fatal(err)
		}
		complete, err := filterLogs(strings.NewReader(input.String()), &output, opts, stats)
		if err != nil || complete || output.String() != expected.String() || stats.stopReason != "limit" ||
			stats.matched != int64(opts.lines) || stats.written != int64(opts.lines) || stats.scannedLines != int64(opts.lines*2) {
			t.Fatalf("统计改变了数量、反转顺序或停止条件：args=%v stats=%+v err=%v", args, stats, err)
		}
	}
}

func TestDebugEmptyInput(t *testing.T) {
	stats := &queryStats{started: time.Now()}
	var output, diagnostics bytes.Buffer
	complete, err := filterLogs(strings.NewReader(""), &output, testOptions(t), stats)
	if err != nil || !complete || output.Len() != 0 || stats.scannedLines != 0 || stats.matched != 0 || stats.stopReason != "eof" {
		t.Fatalf("空输入统计错误：%+v err=%v", stats, err)
	}
	if err := stats.report(&diagnostics, nil, stats.stopReason); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diagnostics.String(), "line=n/a match=n/a output=n/a") {
		t.Fatal("空输入不应产生首行、首匹配或首输出时间")
	}
}

func TestQueryDebugCancellationAndOutputErrors(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprint(canceled), func(t *testing.T) {
			fakeJournal(t, "hold")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			outputErr := errors.New("output closed")
			writer := callbackWriter{write: func() error {
				if canceled {
					cancel()
					return nil
				}
				return outputErr
			}}
			var diagnostics bytes.Buffer
			err := query(ctx, testOptions(t, "--debug", "-f"), writer, &diagnostics)
			wantErr, outcome := outputErr, "error"
			if canceled {
				wantErr, outcome = context.Canceled, "canceled"
			}
			if !errors.Is(err, wantErr) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				t.Fatalf("诊断改变了取消或输出错误：%v", err)
			}
			if !strings.Contains(diagnostics.String(), "stop="+outcome) {
				t.Fatalf("诊断未反映实际停止原因：%s", diagnostics.String())
			}
		})
	}
}

func TestQueryDebugMissingJournal(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var diagnostics bytes.Buffer
	if err := query(context.Background(), testOptions(t, "--debug"), io.Discard, &diagnostics); err == nil {
		t.Fatal("缺少 journalctl 时应返回错误")
	}
	if !strings.Contains(diagnostics.String(), "stop=error") ||
		!strings.Contains(diagnostics.String(), "line=n/a match=n/a output=n/a") {
		t.Fatalf("启动失败时的统计错误：%s", diagnostics.String())
	}
}

func TestQueryDebugReportErrors(t *testing.T) {
	fakeJournal(t, "success")
	reportErr := errors.New("diagnostics closed")
	outputErr := errors.New("output closed")
	for _, failOutput := range []bool{false, true} {
		var output io.Writer = io.Discard
		if failOutput {
			output = callbackWriter{write: func() error { return outputErr }}
		}
		diagnostics := callbackWriter{write: func() error { return reportErr }}
		err := query(context.Background(), testOptions(t, "--debug"), output, diagnostics)
		if !errors.Is(err, reportErr) || (failOutput && !errors.Is(err, outputErr)) {
			t.Fatalf("诊断写入错误或原始查询错误被丢弃：%v", err)
		}
	}
}

type delayedDebugReader struct {
	input   io.Reader
	elapsed time.Duration
}

func (reader *delayedDebugReader) Read(data []byte) (int, error) {
	start := time.Now()
	time.Sleep(2 * time.Millisecond)
	reader.elapsed += time.Since(start)
	return reader.input.Read(data)
}

func TestDebugReadAndWriteAttribution(t *testing.T) {
	reader := &delayedDebugReader{input: strings.NewReader(sampleLine("wanted", "local-a", "rpc") + "\n")}
	var writeElapsed time.Duration
	writer := callbackWriter{write: func() error {
		start := time.Now()
		time.Sleep(2 * time.Millisecond)
		writeElapsed += time.Since(start)
		return nil
	}}
	stats := &queryStats{started: time.Now()}
	complete, err := filterLogs(reader, writer, testOptions(t, "--json", "-n", "0"), stats)
	if err != nil || !complete {
		t.Fatal(err)
	}
	if stats.readTime < reader.elapsed || stats.writeTime < writeElapsed {
		t.Fatalf("IO 等待未计入对应阶段：read=%s write=%s", stats.readTime, stats.writeTime)
	}
}

func BenchmarkFilterDebug(b *testing.B) {
	data := bytes.Repeat(queryFixture("nested4k", 1), 100)
	for _, mode := range []string{"json", "fields"} {
		args := []string{"-n", "0"}
		if mode == "json" {
			args = append(args, "--json")
		} else {
			args = append(args, "-k", "needle")
		}
		opts := benchmarkOptions(b, args...)
		for _, debug := range []bool{false, true} {
			b.Run(fmt.Sprintf("%s/debug=%v", mode, debug), func(b *testing.B) {
				b.SetBytes(int64(len(data)))
				b.ReportAllocs()
				for index := 0; index < b.N; index++ {
					var stats *queryStats
					if debug {
						stats = &queryStats{started: time.Now()}
					}
					if _, err := filterLogs(bytes.NewReader(data), io.Discard, opts, stats); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
