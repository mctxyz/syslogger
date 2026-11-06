package hourly

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	_ "time/tzdata"
)

func useLocation(t *testing.T, location *time.Location) {
	t.Helper()
	previous := time.Local
	time.Local = location
	t.Cleanup(func() { time.Local = previous })
}

func assertWindow(t *testing.T, got Window, starts [Hours]time.Time, counts [Hours]uint64) {
	t.Helper()
	for index := range got {
		if !got[index].StartTime.Equal(starts[index]) || got[index].StartTime.Location() != time.UTC || got[index].Count != counts[index] {
			t.Fatalf("小时桶不符：index=%d got=%+v wantStart=%v wantCount=%d", index, got[index], starts[index], counts[index])
		}
	}
}

func TestWindowHourlyBoundariesAndExpiration(t *testing.T) {
	useLocation(t, time.UTC)
	base := time.Date(2026, 11, 6, 13, 0, 0, 0, time.UTC)
	var window Window
	window.Add(base.Add(-2*time.Hour+time.Minute), 10)
	window.Add(base.Add(-time.Nanosecond), 20)
	window.Add(base, 30)
	window.Add(base.Add(25*time.Minute), 40)
	assertWindow(t, window,
		[Hours]time.Time{base.Add(-2 * time.Hour), base.Add(-time.Hour), base},
		[Hours]uint64{10, 20, 70},
	)
	window.Add(base.Add(time.Hour), 1)
	assertWindow(t, window,
		[Hours]time.Time{base.Add(-time.Hour), base, base.Add(time.Hour)},
		[Hours]uint64{20, 70, 1},
	)
	window.Add(base.Add(4*time.Hour), 2)
	assertWindow(t, window,
		[Hours]time.Time{base.Add(2 * time.Hour), base.Add(3 * time.Hour), base.Add(4 * time.Hour)},
		[Hours]uint64{0, 0, 2},
	)
	before := window
	view := window.Recent(base.Add(7 * time.Hour))
	assertWindow(t, view,
		[Hours]time.Time{base.Add(5 * time.Hour), base.Add(6 * time.Hour), base.Add(7 * time.Hour)},
		[Hours]uint64{},
	)
	if window != before {
		t.Fatal("只读视图改写了保存的小时快照")
	}
}

func TestMergeDoesNotRegressForStaleCallerTime(t *testing.T) {
	useLocation(t, time.UTC)
	base := time.Date(2026, 11, 6, 13, 0, 0, 0, time.UTC)
	var stored, delta Window
	stored.Add(base.Add(-time.Minute), 5)
	stored.Add(base, 7)
	delta.Add(base.Add(-10*time.Minute), 3)
	stored.Merge(delta, base.Add(-time.Second))
	assertWindow(t, stored,
		[Hours]time.Time{base.Add(-2 * time.Hour), base.Add(-time.Hour), base},
		[Hours]uint64{0, 8, 7},
	)
	var next Window
	next.Add(base.Add(time.Hour), 2)
	stored.Merge(next, base.Add(time.Hour))
	assertWindow(t, stored,
		[Hours]time.Time{base.Add(-time.Hour), base, base.Add(time.Hour)},
		[Hours]uint64{8, 7, 2},
	)
}

func TestMergeRestoresOnlyRetainedHours(t *testing.T) {
	useLocation(t, time.UTC)
	base := time.Date(2026, 11, 6, 13, 0, 0, 0, time.UTC)
	var failed, duringWrite Window
	failed.Add(base.Add(-2*time.Hour), 1)
	failed.Add(base.Add(-time.Hour), 2)
	failed.Add(base, 3)
	duringWrite.Add(base.Add(time.Hour), 4)
	duringWrite.Merge(failed, base.Add(time.Hour+time.Minute))
	assertWindow(t, duringWrite,
		[Hours]time.Time{base.Add(-time.Hour), base, base.Add(time.Hour)},
		[Hours]uint64{2, 3, 4},
	)
}

func TestMidnightAndJSONCountPrecision(t *testing.T) {
	useLocation(t, time.UTC)
	base := time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)
	var window Window
	window.Add(base, ^uint64(0))
	assertWindow(t, window,
		[Hours]time.Time{base.Add(-2 * time.Hour), base.Add(-time.Hour), base},
		[Hours]uint64{0, 0, ^uint64(0)},
	)
	data, err := json.Marshal(window)
	if err != nil || !strings.Contains(string(data), `"start_time":"2026-11-06T00:00:00Z","count":18446744073709551615`) {
		t.Fatalf("小时标识或 uint64 精度不符：%s, %v", data, err)
	}
	window.Add(base.Add(time.Minute), 1)
	if window[Hours-1].Count != 0 || !window[Hours-1].StartTime.Equal(base) {
		t.Fatal("小时数量回绕后丢失了时段标识")
	}
}

func TestLocalHourWithFractionalUTCOffset(t *testing.T) {
	location := time.FixedZone("local", 5*60*60+45*60)
	useLocation(t, location)
	at := time.Date(2026, 11, 6, 0, 40, 0, 123, time.UTC)
	start := time.Date(2026, 11, 6, 0, 15, 0, 0, time.UTC)
	if got := Start(at); !got.Equal(start) {
		t.Fatalf("未按本地整点分桶：got=%v want=%v", got, start)
	}
	assertWindow(t, (Window{}).Recent(at),
		[Hours]time.Time{start.Add(-2 * time.Hour), start.Add(-time.Hour), start},
		[Hours]uint64{},
	)
}

func TestNewYorkRepeatedAndSkippedHours(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	useLocation(t, location)
	first := time.Date(2026, 11, 1, 5, 20, 0, 0, time.UTC)
	second := first.Add(time.Hour)
	var window Window
	window.Add(first, 1)
	window.Add(second, 2)
	base := first.Truncate(time.Hour)
	assertWindow(t, window,
		[Hours]time.Time{base.Add(-time.Hour), base, base.Add(time.Hour)},
		[Hours]uint64{0, 1, 2},
	)
	if window[1].StartTime.Local().Hour() != 1 || window[2].StartTime.Local().Hour() != 1 {
		t.Fatal("夹具未覆盖两个实际起点不同的本地 01 点")
	}
	spring := time.Date(2026, 3, 8, 7, 20, 0, 0, time.UTC)
	base = spring.Truncate(time.Hour)
	assertWindow(t, (Window{}).Recent(spring),
		[Hours]time.Time{base.Add(-2 * time.Hour), base.Add(-time.Hour), base},
		[Hours]uint64{},
	)
}

func TestLordHoweHalfHourTransitions(t *testing.T) {
	location, err := time.LoadLocation("Australia/Lord_Howe")
	if err != nil {
		t.Fatal(err)
	}
	useLocation(t, location)
	fall := time.Date(2026, 4, 4, 15, 15, 0, 0, time.UTC)
	fallStart := time.Date(2026, 4, 4, 14, 0, 0, 0, time.UTC)
	if got := Start(fall); !got.Equal(fallStart) {
		t.Fatalf("重复半小时被拆成了额外时段：%v", got)
	}
	spring := time.Date(2026, 10, 3, 15, 45, 0, 0, time.UTC)
	springStart := time.Date(2026, 10, 3, 15, 30, 0, 0, time.UTC)
	assertWindow(t, (Window{}).Recent(spring),
		[Hours]time.Time{springStart.Add(-2 * time.Hour), springStart.Add(-time.Hour), springStart},
		[Hours]uint64{},
	)
}
