package logging

import "github.com/mctxyz/syslogger/internal/diagnostic"

// queueGroup 独占一个等级组的缓冲、连接及发送诊断，I/O 不持有组件协调锁。
type queueGroup struct {
	entries   chan *Entry
	transport transport
	reporter  diagnostic.Reporter
}

func (g *queueGroup) run(q *Queue) {
	defer q.sending.Done()
	defer g.transport.close()
	for entry := range g.entries {
		if q.ctx.Err() != nil {
			q.mu.Lock()
			q.abandoned++
			q.mu.Unlock()
			q.pending.Add(-1)
			continue
		}
		if err := g.transport.send(q.ctx, entry); err != nil {
			q.mu.Lock()
			q.failed.Add(1)
			if q.closing {
				q.closeFailures++
			}
			q.mu.Unlock()
			g.reporter.Printf("%s group=%q: syslog 发送失败，当前条目已放弃: %v", q.identity, g.transport.group.Name(), err)
		}
		q.pending.Add(-1)
	}
}
