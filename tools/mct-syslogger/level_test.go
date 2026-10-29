package main

import (
	"bytes"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestLevelThreshold(t *testing.T) {
	// 从低到高独立定义预期顺序，覆盖 syslog 数值越小、严重程度越高的比较方向。
	severityOrder := []string{"debug", "info", "notice", "warning", "error", "critical", "alert", "emergency"}
	record, _ := parseRecord([]byte(sampleLine("message", "local-a", "rpc")))
	for _, flag := range []string{"-l", "--level"} {
		for minimum, threshold := range severityOrder {
			t.Run(flag+"="+threshold, func(t *testing.T) {
				opts := testOptions(t, flag, threshold)
				for severity, level := range severityOrder {
					record.Level = level
					want := severity >= minimum
					if got := opts.matches(record); got != want {
						t.Fatalf("阈值 %s 匹配 %s：got=%v want=%v", threshold, level, got, want)
					}
				}
			})
		}
	}
}

func TestLevelComposition(t *testing.T) {
	record, _ := parseRecord([]byte(sampleLine("message", "local-a", "rpc")))
	for _, test := range []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "default-all",
			want: []string{"emergency", "alert", "critical", "error", "warning", "notice", "info", "debug"},
		},
		{
			name: "include-exact",
			args: []string{"--include-level", "info"},
			want: []string{"info"},
		},
		{
			name: "repeated-includes",
			args: []string{"--include-level", "error", "--include-level", "info", "--include-level", "error"},
			want: []string{"error", "info"},
		},
		{
			name: "threshold-and-include",
			args: []string{
				"-l", "warning",
				"--include-level", "info",
				"--include-level", "warning",
				"--include-level", "error",
			},
			want: []string{"error", "warning"},
		},
		{
			name: "threshold-and-exclude",
			args: []string{"-l", "error", "--exclude-level", "error"},
			want: []string{"emergency", "alert", "critical"},
		},
		{
			name: "threshold-include-exclude",
			args: []string{
				"-l", "warning",
				"--include-level", "info",
				"--include-level", "error",
				"--include-level", "critical",
				"--exclude-level", "critical",
				"--exclude-level", "info",
			},
			want: []string{"error"},
		},
		{
			name: "include-below-threshold",
			args: []string{"--level", "warning", "--include-level", "info"},
		},
		{
			name: "include-excluded",
			args: []string{"--include-level", "error", "--exclude-level", "error"},
		},
		{
			name: "exclude-all-above-threshold",
			args: []string{"--level", "alert", "--exclude-level", "emergency", "--exclude-level", "alert"},
		},
		{
			name: "last-threshold-stricter",
			args: []string{"--level", "info", "-l", "critical"},
			want: []string{"emergency", "alert", "critical"},
		},
		{
			name: "last-threshold-lower",
			args: []string{"-l", "critical", "--level", "info"},
			want: []string{"emergency", "alert", "critical", "error", "warning", "notice", "info"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			opts := testOptions(t, test.args...)
			var got []string
			for _, level := range levels {
				record.Level = level
				if opts.matches(record) {
					got = append(got, level)
				}
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("组合等级筛选错误：got=%v want=%v", got, test.want)
			}
		})
	}
}

// TestLevelFiltersBeforeLimit 验证阈值、包含及排除在匹配计数之前生效，查询和跟踪保持原有顺序。
func TestLevelFiltersBeforeLimit(t *testing.T) {
	for _, filter := range []struct {
		name       string
		args       []string
		wantRecent []string
		wantFollow []string
	}{
		{
			name:       "exclude",
			args:       []string{"--exclude-level", "debug"},
			wantRecent: []string{"4", "5"},
			wantFollow: []string{"1", "2"},
		},
		{
			name:       "include",
			args:       []string{"--include-level", "error"},
			wantRecent: []string{"1", "4"},
			wantFollow: []string{"1", "4"},
		},
		{
			name:       "threshold",
			args:       []string{"--level", "critical"},
			wantRecent: []string{"2", "5"},
			wantFollow: []string{"2", "5"},
		},
		{
			name: "combined",
			args: []string{
				"-l", "info",
				"--include-level", "error",
				"--include-level", "critical",
				"--exclude-level", "critical",
			},
			wantRecent: []string{"1", "4"},
			wantFollow: []string{"1", "4"},
		},
	} {
		for _, test := range []struct {
			name   string
			follow bool
		}{
			{name: "recent"},
			{name: "follow", follow: true},
		} {
			t.Run(filter.name+"/"+test.name, func(t *testing.T) {
				var input strings.Builder
				eventLevels := []string{"debug", "error", "critical"}
				for position := 0; position < 6; position++ {
					number := 6 - position
					if test.follow {
						number = position + 1
					}
					line := sampleLine(fmt.Sprint(number), "local-a", "rpc")
					level := eventLevels[number%len(eventLevels)]
					line = strings.Replace(line, `"level":"error"`, `"level":`+strconv.Quote(level), 1)
					fmt.Fprintln(&input, line)
				}
				args := append([]string{"-n", "2", "--json"}, filter.args...)
				if test.follow {
					args = append(args, "-f")
				}
				var output bytes.Buffer
				complete, err := filterLogs(strings.NewReader(input.String()), &output, testOptions(t, args...))
				if err != nil || complete {
					t.Fatalf("达到匹配上限时应提前停止: complete=%v err=%v", complete, err)
				}
				var got []string
				for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
					record, valid := parseRecord([]byte(line))
					if !valid {
						t.Fatalf("等级筛选后输出了无效记录: %s", line)
					}
					got = append(got, *record.Message)
				}
				want := filter.wantRecent
				if test.follow {
					want = filter.wantFollow
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("等级筛选后条数或顺序不符: got=%v want=%v", got, want)
				}
			})
		}
	}
}
