package main

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// TestExcludeLevelBeforeLimit 验证等级排除在匹配计数之前生效，并保持查询及跟踪的顺序。
func TestExcludeLevelBeforeLimit(t *testing.T) {
	for _, test := range []struct {
		name   string
		follow bool
		want   []string
	}{
		{name: "recent", want: []string{"3", "5"}},
		{name: "follow", follow: true, want: []string{"1", "3"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var input strings.Builder
			for position := 0; position < 6; position++ {
				number := 6 - position
				if test.follow {
					number = position + 1
				}
				line := sampleLine(fmt.Sprint(number), "local-a", "rpc")
				if number%2 == 0 {
					line = strings.Replace(line, `"level":"error"`, `"level":"debug"`, 1)
				}
				fmt.Fprintln(&input, line)
			}
			args := []string{"--exclude-level", "debug", "-n", "2", "--json"}
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
				if !valid || record.Level != "error" {
					t.Fatalf("排除等级后输出了无效或不匹配的记录: %s", line)
				}
				got = append(got, *record.Message)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("排除等级后条数或顺序不符: got=%v want=%v", got, test.want)
			}
		})
	}
}
