package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

type queryBenchmarkCase struct {
	name string
	kind string
	rate int
	args []string
}

func benchmarkQueryCases() []queryBenchmarkCase {
	var cases []queryBenchmarkCase
	for _, kind := range []string{"small", "nested4k", "blob64k", "nested64k"} {
		for _, rate := range []int{1, 100} {
			cases = append(cases, queryBenchmarkCase{
				name: fmt.Sprintf("%s/hit%d/json", kind, rate),
				kind: kind,
				rate: rate,
				args: []string{"--json"},
			})
		}
		for _, mode := range []struct {
			name string
			args []string
		}{
			{name: "text"},
			{name: "fields", args: []string{"-k", "needle"}},
		} {
			cases = append(cases, queryBenchmarkCase{
				name: kind + "/hit100/" + mode.name,
				kind: kind,
				rate: 100,
				args: mode.args,
			})
		}
	}
	for _, rate := range []int{0, 10} {
		cases = append(cases, queryBenchmarkCase{
			name: fmt.Sprintf("nested4k/hit%d/json", rate),
			kind: "nested4k",
			rate: rate,
			args: []string{"--json"},
		})
	}
	for _, mode := range []struct {
		name string
		args []string
	}{
		{name: "message-json", args: []string{"--json", "-k", "runner started"}},
		{name: "colour", args: []string{"--colour"}},
		{name: "and-ignore-case", args: []string{"-k", "RUNNER", "-k", "NEEDLE", "--ignore-case"}},
		{name: "regex", args: []string{"-e", `"attempt":9007199254740993`}},
		{name: "keyword-miss", args: []string{"-k", "absent"}},
		{name: "regex-miss", args: []string{"-e", "absent"}},
	} {
		cases = append(cases, queryBenchmarkCase{
			name: "nested4k/hit100/" + mode.name,
			kind: "nested4k",
			rate: 100,
			args: mode.args,
		})
	}
	return cases
}

func benchmarkOptions(b *testing.B, args ...string) options {
	b.Helper()
	opts, err := parseOptions(append([]string{"-p", "syslogger-test"}, args...), io.Discard)
	if err != nil {
		b.Fatal(err)
	}
	return opts
}

// BenchmarkQueryRecord 计入基础提取、匹配与格式化；夹具在计时之外构造。
func BenchmarkQueryRecord(b *testing.B) {
	for _, test := range benchmarkQueryCases() {
		lines := make([][]byte, 100)
		for index := range lines {
			line := queryFixture(test.kind, index)
			if index*37%100 >= test.rate {
				line = bytes.Replace(line, []byte("syslogger-test"), []byte("other-project"), 1)
			}
			lines[index] = line
		}
		opts := benchmarkOptions(b, test.args...)
		for _, implementation := range []string{"reference", "go-json"} {
			b.Run(test.name+"/"+implementation, func(b *testing.B) {
				b.SetBytes(int64(len(lines[0])))
				b.ReportAllocs()
				b.ResetTimer()
				for index := 0; index < b.N; index++ {
					line := lines[index%len(lines)]
					if implementation == "reference" {
						if record, valid := parseReferenceRecord(line); valid && record.matches(opts) {
							queryOutputSink = record.format(opts.asJSON, opts.colour)
						}
						continue
					}
					record, valid := parseRecord(line)
					if !valid {
						b.Fatal("夹具无效")
					}
					matched, err := opts.matches(&record)
					if err != nil {
						b.Fatal(err)
					}
					if matched {
						text, err := record.format(opts.asJSON, opts.colour)
						if err != nil {
							b.Fatal(err)
						}
						queryOutputSink = text
					}
				}
			})
		}
	}
}

var queryOutputSink string

// firstOutputWriter 记录一次查询第一次可见写入的延迟，并丢弃合成输出。
type firstOutputWriter struct {
	start time.Time
	first time.Duration
}

func (writer *firstOutputWriter) Write(data []byte) (int, error) {
	if writer.first == 0 {
		writer.first = time.Since(writer.start)
	}
	return len(data), nil
}

// BenchmarkFilterLogs 覆盖真实行读取、最近 N 缓存反转、输出及最后一行没有换行的路径。
func BenchmarkFilterLogs(b *testing.B) {
	for _, mode := range []struct {
		name string
		args []string
	}{
		{name: "recent5-json", args: []string{"-n", "5", "--json"}},
		{name: "recent100-text"},
		{name: "all-fields", args: []string{"-n", "0", "-k", "needle"}},
		{name: "follow-json", args: []string{"-f", "--json"}},
		{name: "follow5-colour", args: []string{"-f", "-n", "5", "--colour"}},
	} {
		var input bytes.Buffer
		for index := 0; index < 256; index++ {
			input.WriteString("ordinary journal message\n")
			line := queryFixture("nested4k", index)
			if index%4 == 0 {
				line = bytes.Replace(line, []byte("syslogger-test"), []byte("other-project"), 1)
			}
			input.Write(line)
		}
		input.Write(queryFixture("blob64k", 256))
		data := bytes.TrimSuffix(input.Bytes(), []byte("\n"))
		opts := benchmarkOptions(b, mode.args...)
		for _, implementation := range []struct {
			name string
			run  func(io.Reader, io.Writer, options) (bool, error)
		}{
			{name: "reference", run: referenceFilterLogs},
			{name: "go-json", run: filterLogs},
		} {
			b.Run(mode.name+"/"+implementation.name, func(b *testing.B) {
				// 限量查询实际只读取前缀，按同一读取行为报告吞吐字节数。
				reader := bytes.NewReader(data)
				var expected bytes.Buffer
				_, err := implementation.run(reader, &expected, opts)
				if err != nil || expected.Len() == 0 {
					b.Fatalf("基准夹具无输出：%v", err)
				}
				b.SetBytes(int64(len(data) - reader.Len()))
				b.ReportAllocs()
				var first time.Duration
				b.ResetTimer()
				for index := 0; index < b.N; index++ {
					writer := firstOutputWriter{start: time.Now()}
					_, err := implementation.run(bytes.NewReader(data), &writer, opts)
					if err != nil {
						b.Fatal(err)
					}
					first += writer.first
				}
				b.StopTimer()
				b.ReportMetric(float64(first.Nanoseconds())/float64(b.N), "first-write-ns/op")
			})
		}
	}
}

// TestQueryPeakMemory 供外部 time -v 测量独立测试进程峰值 RSS，仅处理合成内存日志。
func TestQueryPeakMemory(t *testing.T) {
	implementation := os.Getenv("SYSLOGGER_QUERY_MEMORY")
	if implementation == "" {
		t.Skip("仅用于独立测试进程的峰值内存测量")
	}
	data := bytes.Repeat(queryFixture("nested64k", 1), 512)
	opts := testOptions(t, "-k", "needle")
	var err error
	switch implementation {
	case "reference":
		_, err = referenceFilterLogs(bytes.NewReader(data), io.Discard, opts)
	case "go-json":
		_, err = filterLogs(bytes.NewReader(data), io.Discard, opts)
	default:
		t.Fatal("未知测试实现：" + strings.TrimSpace(implementation))
	}
	if err != nil {
		t.Fatal(err)
	}
}
