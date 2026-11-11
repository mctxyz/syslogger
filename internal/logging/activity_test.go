package logging

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mctxyz/syslogger/internal/journalwire"
)

func activityCount(activity Activity) uint64 {
	var count uint64
	for _, bucket := range activity.LogCounts {
		count += bucket.Count
	}
	return count
}

func TestActivityCountsOnlyAcceptedEntriesAndRestoresWindow(t *testing.T) {
	wake := make(chan struct{}, 1)
	q := bufferedQueue(2)
	q.activityWake = wake
	entry := &Entry{}
	if q.Enqueue(entry) != nil || q.Enqueue(entry) != nil || !errors.Is(q.Enqueue(entry), ErrQueueFull) {
		t.Fatal("未形成预期入队与拒收")
	}
	first := q.TakeActivity()
	if activityCount(first) != 2 || first.FirstAt.IsZero() || first.LastAt.Before(first.FirstAt) || len(wake) != 1 {
		t.Fatalf("入队统计或首次通知不符: %+v", first)
	}
	<-wake
	<-q.groups[journalwire.ErrorGroup].entries
	if err := q.Enqueue(entry); err != nil {
		t.Fatal(err)
	}
	if len(wake) != 0 {
		t.Fatal("取走增量后重复发送了首次活动通知")
	}
	q.RestoreActivity(first)
	combined := q.TakeActivity()
	if activityCount(combined) != 3 || !combined.FirstAt.Equal(first.FirstAt) || combined.LastAt.Before(first.LastAt) {
		t.Fatalf("写入期间新增活动与失败窗口合并不符: %+v", combined)
	}
	if empty := q.TakeActivity(); !empty.FirstAt.IsZero() || activityCount(empty) != 0 {
		t.Fatalf("已取走窗口被重复统计: %+v", empty)
	}
	q.closing = true
	if !errors.Is(q.Enqueue(entry), ErrClosed) || !q.TakeActivity().FirstAt.IsZero() {
		t.Fatal("关闭拒收被计入成功入队量")
	}
}

func TestActivityOverflowStillHasActivity(t *testing.T) {
	q := bufferedQueue(1)
	q.activityWake = make(chan struct{}, 1)
	q.logCounts.Add(time.Now(), ^uint64(0))
	if err := q.Enqueue(&Entry{}); err != nil {
		t.Fatal(err)
	}
	activity := q.TakeActivity()
	if activityCount(activity) != 0 || activity.FirstAt.IsZero() || activity.LastAt.IsZero() {
		t.Fatalf("回绕后的零计数被当成无活动: %+v", activity)
	}
}

func TestActivityConcurrentEnqueueAndSnapshots(t *testing.T) {
	q := bufferedQueue(1000)
	q.activityWake = make(chan struct{}, 1)
	var workers sync.WaitGroup
	for index := 0; index < 10; index++ {
		workers.Add(1)
		go func(level Level) {
			defer workers.Done()
			for count := 0; count < 100; count++ {
				if err := q.Enqueue(&Entry{Severity: level}); err != nil {
					t.Error(err)
				}
			}
		}(Level(index % 8))
	}
	done := make(chan struct{})
	go func() {
		workers.Wait()
		close(done)
	}()
	var total uint64
	for {
		total += activityCount(q.TakeActivity())
		select {
		case <-done:
			total += activityCount(q.TakeActivity())
			if total != 1000 {
				t.Fatalf("并发快照丢失入队量: %d", total)
			}
			return
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func TestActivitySurvivesSendFailure(t *testing.T) {
	q := New(8, filepath.Join(t.TempDir(), "missing.sock"), "p", "i", "rpc", make(chan struct{}, 1))
	if err := q.Enqueue(&Entry{Body: []byte("event")}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for q.pending.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("发送未结束")
		}
		time.Sleep(time.Millisecond)
	}
	q.Stop(context.Background())
	if err := q.Wait(); err != nil {
		t.Fatal(err)
	}
	if activity := q.TakeActivity(); activityCount(activity) != 1 {
		t.Fatalf("发送失败回减了入队量: %+v", activity)
	}
	if _, failed := q.Counts(); failed != 1 {
		t.Fatal("原有发送失败指标发生变化")
	}
}

func TestRestoreActivityKeepsRecentHoursAndOriginalActivity(t *testing.T) {
	now := time.Now().UTC()
	q := &Queue{firstAccepted: now, lastAccepted: now}
	q.logCounts.Add(now, 3)
	old := now.Add(-4 * time.Hour)
	failed := Activity{FirstAt: old, LastAt: old}
	failed.LogCounts.Add(old, 100)
	q.RestoreActivity(failed)
	activity := q.TakeActivity()
	if activityCount(activity) != 3 || !activity.FirstAt.Equal(old) || !activity.LastAt.Equal(now) {
		t.Fatalf("失败恢复保留了过期数量或丢失原始活动：%+v", activity)
	}
}
