package logging

import "time"

// Activity 是一个尚未持久化的成功入队窗口，数量按 uint64 自然回绕。
type Activity struct {
	Count   uint64
	FirstAt time.Time
	LastAt  time.Time
}

// TakeActivity 取走当前窗口，后续入队在新窗口累计，文件 I/O 无需持有队列锁。
func (q *Queue) TakeActivity() Activity {
	q.mu.Lock()
	defer q.mu.Unlock()
	activity := Activity{
		Count:   q.accepted.Swap(0),
		FirstAt: q.firstAccepted,
		LastAt:  q.lastAccepted,
	}
	q.firstAccepted = time.Time{}
	return activity
}

// RestoreActivity 将写入失败的窗口合回期间新增的活动，保持下一周期可重试且不重复提交。
func (q *Queue) RestoreActivity(activity Activity) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.accepted.Add(activity.Count)
	if q.firstAccepted.IsZero() || activity.FirstAt.Before(q.firstAccepted) {
		q.firstAccepted = activity.FirstAt
	}
	if activity.LastAt.After(q.lastAccepted) {
		q.lastAccepted = activity.LastAt
	}
}
