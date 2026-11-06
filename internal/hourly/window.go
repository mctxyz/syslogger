// Package hourly 保存当前自然小时及前两个小时的日志接收量。
package hourly

import "time"

// Hours 是接收量窗口保留的自然小时数，包含尚未结束的当前小时。
const Hours = 3

// Bucket 以小时的实际起始时刻标识数量归属，数量使用 uint64 自然回绕。
type Bucket struct {
	// StartTime 使用 UTC 表示本机自然小时的起始时刻。
	StartTime time.Time `json:"start_time"`
	// Count 表示该小时内成功接收的日志数量。
	Count uint64 `json:"count"`
}

// Window 按起始时刻从旧到新排列三个小时，零值表示尚无计数。
type Window [Hours]Bucket

// Start 返回本机时区中所属自然小时的实际起点，并转换为 UTC。
// 重复小时保留实际偏移；跳时缺失整点时采用该小时第一个存在的时刻。
func Start(at time.Time) time.Time {
	local := at.In(time.Local)
	elapsed := time.Duration(local.Minute())*time.Minute +
		time.Duration(local.Second())*time.Second +
		time.Duration(local.Nanosecond())
	start := at.Add(-elapsed)
	wall := start.In(time.Local)
	if sameHour(wall, local) && wall.Minute() == 0 && wall.Second() == 0 {
		return start.UTC()
	}
	// 半小时偏移跳变可能跨过候选整点，使用本地日期确定仍存在的小时边界。
	start = time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), 0, 0, 0, time.Local)
	if !sameHour(start.In(time.Local), local) {
		start, _ = local.ZoneBounds()
	}
	return start.UTC()
}

func sameHour(a, b time.Time) bool {
	return a.Year() == b.Year() && a.YearDay() == b.YearDay() && a.Hour() == b.Hour()
}

// Recent 按查询时刻生成当前小时及前两个自然小时，缺少的数量补零。
func (window Window) Recent(now time.Time) Window {
	var result Window
	start := Start(now)
	for index := Hours - 1; index >= 0; index-- {
		result[index].StartTime = start
		for _, bucket := range window {
			if bucket.StartTime.Equal(start) {
				result[index].Count += bucket.Count
			}
		}
		if index > 0 {
			start = Start(start.Add(-time.Nanosecond))
		}
	}
	return result
}

// Add 将接收增量归入实际发生的小时，同一小时直接累加。
func (window *Window) Add(at time.Time, count uint64) {
	start := Start(at)
	for index := Hours - 1; index >= 0; index-- {
		if window[index].StartTime.Equal(start) {
			window[index].Count += count
			return
		}
	}
	var delta Window
	delta[Hours-1] = Bucket{StartTime: start, Count: count}
	window.Merge(delta, at)
}

// Merge 按起始时刻合并增量，仅保留合并基准对应的三个自然小时。
// 已观察到的较新小时推进基准，避免旧批次晚落锁时删除已保存的新小时数量。
func (window *Window) Merge(delta Window, now time.Time) {
	for _, source := range [...]Window{*window, delta} {
		for _, bucket := range source {
			if bucket.StartTime.After(now) {
				now = bucket.StartTime
			}
		}
	}
	result := window.Recent(now)
	for _, incoming := range delta {
		for index := range result {
			if result[index].StartTime.Equal(incoming.StartTime) {
				result[index].Count += incoming.Count
				break
			}
		}
	}
	*window = result
}
