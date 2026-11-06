package hostinit

import (
	"testing"

	"github.com/mctxyz/syslogger/internal/journalwire"
)

func TestCapacityAndOptions(t *testing.T) {
	for input, want := range map[string]uint64{"1": 1, "5G": 5 << 30, "5120M": 5 << 30, "1T": 1 << 40} {
		got, err := sizeBytes(input)
		if err != nil || got != want {
			t.Fatalf("容量 %s: got=%d want=%d err=%v", input, got, want, err)
		}
	}
	for _, input := range []string{"", "0", "-1G", "5G\nCompress=no", "16E", "999999999999999999999G", "1GB", " 5G"} {
		if _, err := sizeBytes(input); err == nil {
			t.Fatalf("接受了无效容量 %q", input)
		}
	}
	for _, opts := range []Options{
		{JournalMaxUse: [journalwire.GroupCount]string{"1G", "2G", "1G", "1G"}},
		{User: "app\nroot", JournalMaxUse: [journalwire.GroupCount]string{"1G", "2G", "1G", "1G"}},
		{User: "app"},
	} {
		if err := opts.Validate(); err == nil {
			t.Fatalf("接受了无效选项 %+v", opts)
		}
	}
	for group := journalwire.DebugGroup; group < journalwire.GroupCount; group++ {
		opts := Options{User: "app"}
		for current := journalwire.DebugGroup; current < journalwire.GroupCount; current++ {
			opts.JournalMaxUse[current] = DefaultJournalMaxUse(current)
		}
		if err := opts.Validate(); err != nil {
			t.Fatal(err)
		}
		opts.JournalMaxUse[group] = "0"
		if err := opts.Validate(); err == nil {
			t.Fatalf("未校验 %s 空间容量", group.Name())
		}
	}
}
