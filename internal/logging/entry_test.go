package logging

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/mctxyz/syslogger/internal/identity"
)

func TestEncodeSnapshotAndErrors(t *testing.T) {
	fields := map[string]any{"error": errors.New("详情"), "values": []int{1, 2}}
	entry, err := Encode(identity.Instance{Project: "demo", InstanceID: "common"}, "rpc", Error, "消息\n内容", fields, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	fields["values"].([]int)[0] = 99
	if !json.Valid(entry.Body) || strings.Contains(string(entry.Body), "\n") || !strings.Contains(string(entry.Body), `"values":[1,2]`) {
		t.Fatalf("编码冻结失败: %s", entry.Body)
	}
	for _, value := range []any{make(chan int), math.NaN(), func() {}} {
		if _, err := Encode(identity.Instance{}, "rpc", Info, "", map[string]any{"value": value}, time.Now()); err == nil {
			t.Fatal("不可编码值被接收")
		}
	}
	if _, err := Encode(identity.Instance{}, "rpc", Level(8), "", nil, time.Now()); err == nil {
		t.Fatal("无效内部等级未拒绝")
	}
}

func BenchmarkEncode(b *testing.B) {
	for _, size := range []int{512, 4096} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			message := strings.Repeat("x", size)
			id := identity.Instance{Project: "bench", InstanceID: "common"}
			b.ReportAllocs()
			for n := 0; n < b.N; n++ {
				if _, err := Encode(id, "rpc", Info, message, nil, time.Now()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
