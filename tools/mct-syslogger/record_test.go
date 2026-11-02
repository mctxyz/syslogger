package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"testing/iotest"
)

func queryFixture(kind string, sequence int) []byte {
	fields := map[string]any{
		"runner":   "binance_square_monitor",
		"attempt":  json.Number("9007199254740993"),
		"detail":   "请求&失败 needle",
		"sequence": sequence,
	}
	items := 0
	switch kind {
	case "nested4k":
		items = 32
	case "nested64k":
		items = 640
	case "blob64k":
		fields["blob"] = strings.Repeat("x", 65536)
	}
	if items > 0 {
		values := make([]any, items)
		for index := range values {
			values[index] = map[string]any{
				"id":     index,
				"text":   strings.Repeat("x", 48),
				"values": []any{1, "中", true, nil},
			}
		}
		fields["items"] = values
	}
	line, err := json.Marshal(map[string]any{
		"timestamp":   "2026-10-29T02:59:24.123456789Z",
		"level":       "info",
		"project":     "syslogger-test",
		"instance_id": "listener-v2",
		"component":   "collector",
		"message":     "runner started",
		"fields":      fields,
	})
	if err != nil {
		panic(err)
	}
	return append(line, '\n')
}

func queryParityCases() map[string]string {
	base := strings.TrimSpace(string(queryFixture("small", 1)))
	return map[string]string{
		"normal":              base,
		"nested":              strings.TrimSpace(string(queryFixture("nested4k", 1))),
		"plain":               "Started background service.",
		"empty":               "",
		"null":                "null",
		"array":               "[]",
		"truncated":           base[:len(base)-1],
		"trailing-value":      base + " {}",
		"trailing-unicode":    base + "\u00a0\u2028",
		"leading-unicode":     "\u00a0" + base,
		"leading-json-space":  " \t\r\n" + base,
		"missing-project":     strings.Replace(base, `"project":"syslogger-test",`, "", 1),
		"folded-key":          strings.Replace(base, `"project":`, `"PROJECT":`, 1),
		"unicode-folded-key":  strings.Replace(base, `"instance_id":`, `"inſtance_id":`, 1),
		"unicode-fields-key":  strings.Replace(base, `"fields":`, `"fieldſ":`, 1),
		"escaped-key":         strings.Replace(base, `"project":`, `"pro\u006aect":`, 1),
		"duplicate-match":     strings.Replace(base, `"project":"syslogger-test"`, `"project":"other","project":"syslogger-test"`, 1),
		"duplicate-reject":    strings.Replace(base, `"project":"syslogger-test"`, `"project":"syslogger-test","project":"other"`, 1),
		"null-after-project":  strings.Replace(base, `"project":"syslogger-test"`, `"project":"syslogger-test","project":null`, 1),
		"empty-message":       strings.Replace(base, `"runner started"`, `""`, 1),
		"null-message":        strings.Replace(base, `"runner started"`, `null`, 1),
		"numeric-message":     strings.Replace(base, `"runner started"`, `123`, 1),
		"null-fields":         strings.TrimSuffix(base, "}") + `,"fields":null}`,
		"empty-fields":        strings.TrimSuffix(base, "}") + `,"fields":null,"fields":{}}`,
		"array-fields":        strings.TrimSuffix(base, "}") + `,"fields":[]}`,
		"numeric-fields":      strings.TrimSuffix(base, "}") + `,"fields":1}`,
		"string-fields":       strings.TrimSuffix(base, "}") + `,"fields":"text"}`,
		"boolean-fields":      strings.TrimSuffix(base, "}") + `,"fields":false}`,
		"absent-fields":       `{"timestamp":"t","level":"info","project":"syslogger-test","instance_id":"listener-v2","component":"collector","message":"ok"}`,
		"merge-fields":        strings.TrimSuffix(base, "}") + `,"fields":{"preserved":1},"FIELDS":{"later":2}}`,
		"replace-nested":      strings.TrimSuffix(base, "}") + `,"fields":{"nested":{"first":1}},"fields":{"nested":{"last":2}}}`,
		"reset-fields":        strings.TrimSuffix(base, "}") + `,"fields":null,"fields":{"last":2}}`,
		"invalid-number":      strings.Replace(base, `"sequence":1`, `"sequence":01`, 1),
		"huge-exponent":       strings.Replace(base, `9007199254740993`, `1e1000`, 1),
		"invalid-surrogate":   strings.Replace(base, `"runner started"`, `"\ud800"`, 1),
		"invalid-utf8":        strings.Replace(base, `runner started`, string([]byte{'x', 0xff}), 1),
		"invalid-utf8-fields": strings.Replace(base, `needle`, string([]byte{'x', 0xff}), 1),
		"invalid-utf8-key":    strings.Replace(base, `"detail":`, `"`+string([]byte{0xff})+`":`, 1),
		"escaped-field-key":   strings.Replace(base, `"detail":`, `"\u2028<tag&>":`, 1),
		"separators":          strings.Replace(base, `needle`, `needle\u2028\u2029`, 1),
		"unknown-number":      strings.TrimSuffix(base, "}") + `,"unknown":01}`,
		"unknown-escape":      strings.TrimSuffix(base, "}") + `,"unknown":"\q"}`,
		"unknown-exponent":    strings.TrimSuffix(base, "}") + `,"unknown":1e1000}`,
		"fields-escape":       strings.TrimSuffix(base, "}") + `,"fields":{"bad":"\q"}}`,
		"earlier-type-error":  strings.Replace(base, `"project":"syslogger-test"`, `"project":1,"project":"syslogger-test"`, 1),
		"earlier-fields-type": strings.TrimSuffix(base, "}") + `,"fields":3,"fields":{}}`,
		"unknown-depth":       strings.TrimSuffix(base, "}") + `,"unknown":` + strings.Repeat("[", 10000) + `0` + strings.Repeat("]", 10000) + `}`,
	}
}

func parityOptions(t testing.TB) []options {
	t.Helper()
	var result []options
	for _, args := range [][]string{
		{"--json"},
		{"--colour"},
		{"-p", "syslogger-test"},
		{"-p", "other", "--json"},
		{"-i", "listener-v2", "-c", "collector", "-l", "info"},
		{"--exclude-level", "info"},
		{"-k", "preserved"},
		{"-k", "9007199254740993"},
		{"-k", "请求&失败"},
		{"-k", "RUNNER", "-k", "NEEDLE", "--ignore-case"},
		{"-k", "runner started", "--json"},
		{"-k", "missing", "--json"},
		{"-k", ""},
		{"-e", `"attempt":9007199254740993`},
		{"-e", `^runner started$`, "--json"},
		{"-e", `^$`},
		{"-e", `(?i)needle`, "-k", "runner", "--colour"},
	} {
		opts, err := parseOptions(args, &bytes.Buffer{})
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, opts)
	}
	return result
}

func compareRecord(t testing.TB, line []byte, queries []options) {
	t.Helper()
	want, wantValid := parseReferenceRecord(line)
	for index, opts := range queries {
		got, valid := parseRecord(line)
		if valid != wantValid {
			t.Fatalf("合法性不同：got=%v want=%v input=%q", valid, wantValid, line)
		}
		if !valid {
			continue
		}
		matched, err := opts.matches(&got)
		if err != nil {
			t.Fatalf("查询 %d：%v input=%q", index, err, line)
		}
		if matched != want.matches(opts) {
			t.Fatalf("查询 %d 匹配不同：got=%v input=%q", index, matched, line)
		}
		if matched {
			text, err := got.format(opts.asJSON, opts.colour)
			if err != nil {
				t.Fatal(err)
			}
			if expected := want.format(opts.asJSON, opts.colour); text != expected {
				t.Fatalf("查询 %d 输出不同：got=%q want=%q input=%q", index, text, expected, line)
			}
		}
	}
}

func TestQueryRecordParity(t *testing.T) {
	queries := parityOptions(t)
	cases := queryParityCases()
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			compareRecord(t, []byte(line), queries)
		})
	}
	t.Logf("记录语义对照：%d 条输入 × %d 类查询 = %d 项", len(cases), len(queries), len(cases)*len(queries))
}

func TestFieldsDemandAndReuse(t *testing.T) {
	for _, test := range []struct {
		name    string
		args    []string
		matched bool
		loaded  bool
	}{
		{name: "base-reject", args: []string{"-p", "other"}},
		{name: "json", args: []string{"--json"}, matched: true},
		{name: "message-json", args: []string{"--json", "-k", "runner", "-e", "started"}, matched: true},
		{name: "readable", matched: true, loaded: true},
		{name: "message-readable", args: []string{"-k", "runner"}, matched: true, loaded: true},
		{name: "fields-readable", args: []string{"-k", "needle", "-e", "attempt"}, matched: true, loaded: true},
		{name: "fields-json", args: []string{"--json", "-k", "needle"}, matched: true, loaded: true},
		{name: "fields-miss", args: []string{"--json", "-k", "missing"}, loaded: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			record, valid := parseRecord(queryFixture("nested4k", 1))
			if !valid || record.fieldsText != nil {
				t.Fatal("基础提取不应展开 fields")
			}
			opts := testOptions(t, test.args...)
			matched := recordMatches(t, opts, &record)
			if matched != test.matched {
				t.Fatalf("匹配结果错误：%v", matched)
			}
			cached := record.fieldsText
			if matched {
				recordText(t, &record, opts.asJSON, opts.colour)
			}
			if (record.fieldsText != nil) != test.loaded {
				t.Fatal("fields 展开时机错误")
			}
			if cached != nil && cached != record.fieldsText {
				t.Fatal("搜索和显示未复用同一缓存")
			}
			if test.loaded {
				cached = record.fieldsText
				// 缓存建成后不再依赖输入，损坏输入也不应触发第二次展开或编码。
				record.line = []byte("invalid")
				if got := recordFields(t, &record); got != *cached || record.fieldsText != cached {
					t.Fatal("fields 被重复展开或编码")
				}
			}
		})
	}
	for _, line := range []string{
		strings.TrimSuffix(sampleLine("message", "local-a", "rpc"), "}") + `,"fields":null}`,
		strings.TrimSuffix(sampleLine("message", "local-a", "rpc"), "}") + `,"fields":null,"fields":{}}`,
	} {
		record, valid := parseRecord([]byte(line))
		if !valid || recordFields(t, &record) != "" || record.fieldsText == nil {
			t.Fatal("空 fields 应缓存空文本")
		}
	}
}

func TestFilterLogsParityAndOwnership(t *testing.T) {
	var input bytes.Buffer
	for index := 0; index < 140; index++ {
		input.WriteString("ordinary journal message\n")
		line := queryFixture("nested4k", index)
		if index%3 == 0 {
			line = bytes.Replace(line, []byte("syslogger-test"), []byte("other-project"), 1)
		}
		input.Write(line)
	}
	input.Write(queryFixture("blob64k", 140))
	data := bytes.TrimSuffix(input.Bytes(), []byte("\n"))
	for _, args := range [][]string{
		{},
		{"-n", "5", "--json"},
		{"-n", "0", "--colour"},
		{"-f", "--json"},
		{"-f", "-n", "5", "-k", "needle"},
	} {
		t.Run(fmt.Sprint(args), func(t *testing.T) {
			opts := testOptions(t, append([]string{"-p", "syslogger-test"}, args...)...)
			var want, got bytes.Buffer
			wantComplete, wantErr := referenceFilterLogs(bytes.NewReader(data), &want, opts)
			complete, err := filterLogs(iotest.HalfReader(bytes.NewReader(data)), &got, opts, nil)
			if err != nil || wantErr != nil || complete != wantComplete || got.String() != want.String() {
				t.Fatalf("读取、缓存顺序或输出不同：complete=%v/%v err=%v/%v", complete, wantComplete, err, wantErr)
			}
		})
	}
}

func FuzzQueryRecordParity(f *testing.F) {
	queries := parityOptions(f)
	for _, line := range queryParityCases() {
		if len(line) < 10000 {
			f.Add([]byte(line))
		}
	}
	f.Fuzz(func(t *testing.T, line []byte) {
		compareRecord(t, line, queries)
	})
}
