package logging

import (
	"time"

	"github.com/mctxyz/syslogger/internal/hourly"
)

// Activity 保存尚未持久化的小时增量及独立活动时间，数量归零不改变接收事实。
type Activity struct {
	LogCounts hourly.Window
	FirstAt   time.Time
	LastAt    time.Time
}

// recordActivity 在队列锁内累计已接收的异步入队或同步发送活动。
func (q *Queue) recordActivity() {
	if q.activityWake == nil {
		return
	}
	at := time.Now().UTC()
	q.logCounts.Add(at, 1)
	if q.firstAccepted.IsZero() {
		q.firstAccepted = at
	}
	if q.lastAccepted.IsZero() {
		select {
		case q.activityWake <- struct{}{}:
		default:
		}
	}
	q.lastAccepted = at
}

// TakeActivity 取走当前窗口，后续接收在新窗口累计，文件 I/O 无需持有队列锁。
func (q *Queue) TakeActivity() Activity {
	q.mu.Lock()
	defer q.mu.Unlock()
	activity := Activity{
		LogCounts: q.logCounts,
		FirstAt:   q.firstAccepted,
		LastAt:    q.lastAccepted,
	}
	q.logCounts = hourly.Window{}
	q.firstAccepted = time.Time{}
	return activity
}

// RestoreActivity 将写入失败的窗口合回期间新增的活动，保持下一周期可重试且不重复提交。
func (q *Queue) RestoreActivity(activity Activity) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.logCounts.Merge(activity.LogCounts, time.Now())
	if q.firstAccepted.IsZero() || activity.FirstAt.Before(q.firstAccepted) {
		q.firstAccepted = activity.FirstAt
	}
	if activity.LastAt.After(q.lastAccepted) {
		q.lastAccepted = activity.LastAt
	}
}
