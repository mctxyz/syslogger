package hostinit

import "testing"

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
		{JournalMaxUse: "5G"},
		{User: "app\nroot", JournalMaxUse: "5G"},
		{User: "app", JournalMaxUse: "0"},
	} {
		if err := opts.Validate(); err == nil {
			t.Fatalf("接受了无效选项 %+v", opts)
		}
	}
}
