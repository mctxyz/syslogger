package main

import (
	"fmt"
	"io"
	"time"
)

// queryStats 由单次查询持有，仅在 --debug 开启时分配；各阶段记录累计墙钟耗时。
// fields 的解码与编码是筛选或格式化的子阶段，单独展示但不重复计入阶段合计。
type queryStats struct {
	started       time.Time
	startTime     time.Duration
	readTime      time.Duration
	parseTime     time.Duration
	filterTime    time.Duration
	formatTime    time.Duration
	writeTime     time.Duration
	waitTime      time.Duration
	fieldsDecode  time.Duration
	fieldsEncode  time.Duration
	firstLine     time.Duration
	firstMatch    time.Duration
	firstOutput   time.Duration
	scannedLines  int64
	scannedBytes  int64
	validRecords  int64
	baseMatches   int64
	matched       int64
	written       int64
	fieldsDecodes int64
	fieldsEncodes int64
	stopReason    string
}

func debugLatency(elapsed time.Duration, count int64) string {
	if count == 0 {
		return "n/a"
	}
	return elapsed.String()
}

// report 在 journalctl 收尾后写入 stderr，避免与子进程的诊断并发写入同一个 writer。
func (stats *queryStats) report(output io.Writer, args []string, outcome string) error {
	total := time.Since(stats.started)
	other := total - stats.startTime - stats.readTime - stats.parseTime - stats.filterTime -
		stats.formatTime - stats.writeTime - stats.waitTime
	_, err := fmt.Fprintf(output,
		"[debug] query: total=%s stop=%s\n"+
			"[debug] journalctl: args=%q\n"+
			"[debug] time: start=%s read=%s parse=%s filter=%s\n"+
			"[debug] time: format=%s write=%s wait=%s other=%s\n"+
			"[debug] first: line=%s match=%s output=%s\n"+
			"[debug] count: scanned_lines=%d scanned_bytes=%d valid=%d base_matches=%d matched=%d written=%d\n"+
			"[debug] fields: decode=%s encode=%s decodes=%d encodes=%d (included in filter/format)\n"+
			"[debug] 说明：read 含等待 journalctl 和行读取；wait 含取消与进程收尾。\n"+
			"[debug] 说明：first 为查询开始后的延迟；跟踪空闲等待计入 read；计时本身有额外开销。\n",
		total, outcome,
		args,
		stats.startTime, stats.readTime, stats.parseTime, stats.filterTime,
		stats.formatTime, stats.writeTime, stats.waitTime, other,
		debugLatency(stats.firstLine, stats.scannedLines),
		debugLatency(stats.firstMatch, stats.matched),
		debugLatency(stats.firstOutput, stats.written),
		stats.scannedLines, stats.scannedBytes, stats.validRecords, stats.baseMatches, stats.matched, stats.written,
		stats.fieldsDecode, stats.fieldsEncode, stats.fieldsDecodes, stats.fieldsEncodes,
	)
	return err
}
