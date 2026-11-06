package logging

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mctxyz/syslogger/internal/diagnostic"
	"github.com/mctxyz/syslogger/internal/hourly"
	"github.com/mctxyz/syslogger/internal/identity"
)

var (
	// ErrClosed 表示日志接收已经结束。
	ErrClosed = errors.New("syslogger: 日志接收已关闭")
	// ErrQueueFull 表示本次输入因队列容量已满而被拒收。
	ErrQueueFull = errors.New("syslogger: 日志队列已满")
)

// Queue 持有模块独立 FIFO、固定发送任务、同步接收截止、失败计数及小时接收活动。
type Queue struct {
	mu          sync.Mutex
	entries     chan *Entry
	closing     bool
	done        chan struct{}
	ctx         context.Context
	cancel      context.CancelFunc
	syncSending sync.WaitGroup
	transport   transport
	reporter    diagnostic.Reporter
	identity    string
	rejected    atomic.Uint64
	failed      atomic.Uint64
	// 小时增量和活动时间由现有队列锁保护，磁盘维护只接收首次活动通知。
	logCounts     hourly.Window
	activityWake  chan<- struct{}
	firstAccepted time.Time
	lastAccepted  time.Time
	pending       atomic.Int64
	abandoned     uint64
	closeFailures uint64
	abortErr      error
}

// New 按调用方已确定的正数容量建立模块有界指针队列，连接在后台处理首条事件时创建。
// activityWake 非 nil 时维护组件接收活动，首次活动只通知后台，调用线程不执行目录 I/O。
func New(capacity int, path, project, instance, component string, activityWake chan<- struct{}) *Queue {
	ctx, cancel := context.WithCancel(context.Background())
	q := &Queue{
		entries: make(chan *Entry, capacity),
		done:    make(chan struct{}),
		ctx:     ctx,
		cancel:  cancel,
		transport: transport{
			path:      path,
			id:        identity.Instance{Project: project, InstanceID: instance},
			component: component,
		},
		identity:     fmt.Sprintf("project=%q instance_id=%q component=%q", project, instance, component),
		activityWake: activityWake,
	}
	go q.run()
	return q
}

// Reject 累计调用线程编码、上下文或生命周期检查阶段的拒收。
func (q *Queue) Reject() { q.rejected.Add(1) }

// Enqueue 立即接收或拒收，已有记录保持 FIFO。
func (q *Queue) Enqueue(entry *Entry) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closing {
		q.Reject()
		return ErrClosed
	}
	q.pending.Add(1)
	select {
	case q.entries <- entry:
		q.recordActivity()
		return nil
	default:
		q.pending.Add(-1)
		q.Reject()
		return ErrQueueFull
	}
}

// Counts 读取独立的拒收和实际发送失败累计值，包含异步及同步交付。
func (q *Queue) Counts() (uint64, uint64) { return q.rejected.Load(), q.failed.Load() }

// Stop 建立接收截止，关闭期限通过取消连接中断尚未完成的日志交付。
func (q *Queue) Stop(ctx context.Context) {
	q.mu.Lock()
	if q.closing {
		q.mu.Unlock()
		return
	}
	q.closing = true
	close(q.entries)
	q.mu.Unlock()
	go func() {
		select {
		case <-q.done:
		case <-ctx.Done():
			q.mu.Lock()
			if q.pending.Load() > 0 {
				q.abortErr = ctx.Err()
				q.cancel()
				q.transport.close()
			}
			q.mu.Unlock()
		}
	}()
}

// Wait 等待发送任务结束并复用收尾结果，历史运行期失败只保留在 Counter 中。
func (q *Queue) Wait() error {
	<-q.done
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.abortErr != nil {
		return fmt.Errorf("syslogger: %s: 关闭交付未完成，放弃 %d 条，发送失败 %d 条: %w", q.identity, q.abandoned, q.closeFailures, q.abortErr)
	}
	if q.closeFailures != 0 {
		return fmt.Errorf("syslogger: %s: 关闭期间 %d 条日志发送失败", q.identity, q.closeFailures)
	}
	return nil
}

func (q *Queue) run() {
	defer close(q.done)
	defer q.cancel()
	defer q.transport.close()
	// 输入关闭后先等待已接收的同步调用，再发布完成；关闭期限仍可通过共享上下文中断它们。
	defer q.syncSending.Wait()
	for entry := range q.entries {
		if q.ctx.Err() != nil {
			q.abandoned++
			q.pending.Add(-1)
			continue
		}
		if err := q.transport.send(q.ctx, entry); err != nil {
			q.mu.Lock()
			q.failed.Add(1)
			if q.closing {
				q.closeFailures++
			}
			q.mu.Unlock()
			q.reporter.Printf("%s: syslog 发送失败，当前条目已放弃: %v", q.identity, err)
		}
		q.pending.Add(-1)
	}
}
