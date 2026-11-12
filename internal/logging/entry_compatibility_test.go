package logging

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/mctxyz/syslogger/internal/identity"
)

type customJSONValue struct{}

func (customJSONValue) MarshalJSON() ([]byte, error) {
	return []byte(" { \"custom\": \"<tag>\\n中文\" } "), nil
}

type customTextValue string

func (value customTextValue) MarshalText() ([]byte, error) {
	return []byte("text:" + value), nil
}

type nilEncodingError struct{}

func (*nilEncodingError) Error() string {
	return "typed nil error"
}

type failedJSONValue struct {
	err error
}

func (value failedJSONValue) MarshalJSON() ([]byte, error) {
	return nil, value.err
}

type malformedJSONValue struct{}

func (malformedJSONValue) MarshalJSON() ([]byte, error) {
	return []byte(`{"unfinished":`), nil
}

func TestEncodeMatchesStandardJSON(t *testing.T) {
	var nilError *nilEncodingError
	var nilMarshaler *customJSONValue
	cycle := map[string]any{}
	cycle["self"] = cycle
	customErr := errors.New("自定义编码失败")
	id := identity.Instance{Project: "项目<>&\n", InstanceID: "实例\x00"}
	at := time.Date(2026, 11, 12, 10, 20, 30, 123456789, time.FixedZone("test", 8*60*60))
	for _, test := range []struct {
		name       string
		fields     map[string]any
		normalized map[string]any
		cause      error
	}{
		{name: "nil"},
		{name: "empty", fields: map[string]any{}},
		{
			name: "strings_and_keys",
			fields: map[string]any{
				"z":       "<>&\u2028\u2029\x00\n\t\"\\中文",
				"a":       "invalid\xffutf8",
				"key\xff": "value",
			},
		},
		{
			name: "numbers_and_containers",
			fields: map[string]any{
				"uint":   uint64(math.MaxUint64),
				"int":    int64(math.MinInt64),
				"float":  []float64{math.SmallestNonzeroFloat64, math.MaxFloat64, math.Copysign(0, -1)},
				"number": json.Number("12345678901234567890.123"),
				"bytes":  []byte{0, 1, 255},
				"nil":    []string(nil),
				"nested": map[string]any{"b": true, "a": []any{nil, "x", 3}},
				"map":    map[int]string{10: "ten", 2: "two"},
			},
		},
		{
			name: "custom_and_raw_values",
			fields: map[string]any{
				"json": customJSONValue{},
				"text": customTextValue("<中文>"),
				"keys": map[customTextValue]int{"b": 2, "a": 1},
				"raw":  json.RawMessage(" { \"html\": \"<>&\" } "),
				"nil":  nilMarshaler,
				"time": at,
			},
		},
		{
			name:       "errors",
			fields:     map[string]any{"error": errors.New("详情<>&"), "nil": nilError},
			normalized: map[string]any{"error": "详情<>&", "nil": nil},
		},
		{name: "channel", fields: map[string]any{"value": make(chan int)}},
		{name: "function", fields: map[string]any{"value": func() {}}},
		{name: "nan", fields: map[string]any{"value": math.NaN()}},
		{name: "infinity", fields: map[string]any{"value": math.Inf(1)}},
		{name: "invalid_number", fields: map[string]any{"value": json.Number("01")}},
		{name: "invalid_time", fields: map[string]any{"value": time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}},
		{name: "cycle", fields: cycle},
		{name: "malformed_json", fields: map[string]any{"value": malformedJSONValue{}}},
		{name: "custom_error", fields: map[string]any{"value": failedJSONValue{err: customErr}}, cause: customErr},
	} {
		t.Run(test.name, func(t *testing.T) {
			fields := test.fields
			if test.normalized != nil {
				fields = test.normalized
			}
			// 标准库独立生成基准字节，覆盖业务值的排序、转义和自定义编码契约。
			want, wantErr := json.Marshal(struct {
				Timestamp time.Time `json:"timestamp"`
				Level     string    `json:"level"`
				identity.Instance
				Component string         `json:"component"`
				Message   string         `json:"message"`
				Fields    map[string]any `json:"fields,omitempty"`
			}{at.UTC(), "info", id, "rpc\n组件", "消息<>&\n\xff", fields})
			entry, err := Encode(id, "rpc\n组件", Info, "消息<>&\n\xff", test.fields, at)
			if (err != nil) != (wantErr != nil) {
				t.Fatalf("编码成功/失败语义改变: got=%v want=%v", err, wantErr)
			}
			if err != nil {
				if entry != nil || test.cause != nil && !errors.Is(err, test.cause) {
					t.Fatalf("编码失败未保留错误原因: entry=%v err=%v", entry, err)
				}
				return
			}
			if !bytes.Equal(entry.Body, want) || entry.Severity != Info {
				t.Fatalf("日志正文或等级改变:\ngot:  %s\nwant: %s", entry.Body, want)
			}
		})
	}
}

func TestEncodedBodiesRemainIndependent(t *testing.T) {
	id := identity.Instance{Project: "demo", InstanceID: "common"}
	fields := map[string]any{"nested": map[string]any{"values": []int{1, 2, 3}}}
	entry, err := Encode(id, "rpc", Warning, "first", fields, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.Clone(entry.Body)
	fields["nested"].(map[string]any)["values"].([]int)[0] = 99
	for index := 0; index < 100; index++ {
		if _, err := Encode(id, "rpc", Info, "next", fields, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(entry.Body, want) {
		t.Fatal("后续编码或字段修改覆盖了已接收的正文")
	}
}
