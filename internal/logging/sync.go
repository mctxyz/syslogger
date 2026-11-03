package logging

import (
	"context"
	"fmt"
)

// SendSync 使用本次调用独占的连接交付事件，接收截止、活动及失败计数与普通队列共用。
func (q *Queue) SendSync(ctx context.Context, entry *Entry) error {
	q.mu.Lock()
	if q.closing {
		q.mu.Unlock()
		q.Reject()
		return ErrClosed
	}
	if err := ctx.Err(); err != nil {
		q.mu.Unlock()
		q.Reject()
		return err
	}
	// 接收截止后不再 Add；发送任务仅在输入 channel 关闭后等待这些调用完成。
	q.syncSending.Add(1)
	q.pending.Add(1)
	q.recordActivity()
	q.mu.Unlock()
	defer q.syncSending.Done()

	sendCtx, cancel := context.WithCancel(ctx)
	stopQueueCancel := context.AfterFunc(q.ctx, cancel)
	defer stopQueueCancel()
	defer cancel()
	tr := transport{
		path:      q.transport.path,
		id:        q.transport.id,
		component: q.transport.component,
	}
	// 取消通过关闭本次独立连接唤醒写入，数据报及 memfd 路径共享同一取消边界。
	stopWrite := context.AfterFunc(sendCtx, tr.close)
	defer stopWrite()
	defer tr.close()
	err := tr.send(sendCtx, entry)
	if err != nil && sendCtx.Err() != nil {
		err = sendCtx.Err()
	}
	q.mu.Lock()
	if err != nil {
		q.failed.Add(1)
		if q.closing {
			q.closeFailures++
		}
	}
	q.pending.Add(-1)
	q.mu.Unlock()
	if err != nil {
		return fmt.Errorf("syslogger: %s: 同步发送失败: %w", q.identity, err)
	}
	return nil
}
