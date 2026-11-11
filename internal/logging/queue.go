package logging

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mctxyz/syslogger/internal/hourly"
	"github.com/mctxyz/syslogger/internal/identity"
	"github.com/mctxyz/syslogger/internal/journalwire"
)

var (
	// ErrClosed 表示日志接收已经结束。
	ErrClosed = errors.New("syslogger: 日志接收已关闭")
	// ErrQueueFull 表示本次输入因队列容量已满而被拒收。
	ErrQueueFull = errors.New("syslogger: 日志队列已满")
)

// Queue 协调组件四组独立 FIFO 的接收截止、交付汇合、失败计数及小时接收活动。
type Queue struct {
	mu          sync.Mutex
	groups      [journalwire.GroupCount]queueGroup
	closing     bool
	done        chan struct{}
	ctx         context.Context
	cancel      context.CancelFunc
	syncSending sync.WaitGroup
	sending     sync.WaitGroup
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

// New 按调用方已确定的每组正数容量建立四组指针队列，各组连接在首条发送时创建。
// activityWake 非 nil 时维护组件接收活动，首次活动只通知后台，调用线程不执行目录 I/O。
func New(capacity int, path, project, instance, component string, activityWake chan<- struct{}) *Queue {
	ctx, cancel := context.WithCancel(context.Background())
	q := &Queue{
		done:         make(chan struct{}),
		ctx:          ctx,
		cancel:       cancel,
		identity:     fmt.Sprintf("project=%q instance_id=%q component=%q", project, instance, component),
		activityWake: activityWake,
	}
	for group := range q.groups {
		q.groups[group].entries = make(chan *Entry, capacity)
		q.groups[group].transport = transport{
			group:     journalwire.Group(group),
			path:      path,
			id:        identity.Instance{Project: project, InstanceID: instance},
			component: component,
		}
	}
	// 先登记全部发送任务，再启动 worker 和汇合，避免空组提前触发完成。
	q.sending.Add(len(q.groups))
	for group := range q.groups {
		go q.groups[group].run(q)
	}
	go q.finish()
	return q
}

// Reject 累计调用线程编码、上下文或生命周期检查阶段的拒收。
func (q *Queue) Reject() { q.rejected.Add(1) }

// Enqueue 按等级组立即接收或拒收，各组独占容量并保持组内 FIFO。
func (q *Queue) Enqueue(entry *Entry) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closing {
		q.Reject()
		return ErrClosed
	}
	q.pending.Add(1)
	select {
	case q.groups[journalwire.GroupForSeverity(uint8(entry.Severity))].entries <- entry:
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
	for group := range q.groups {
		close(q.groups[group].entries)
	}
	q.mu.Unlock()
	go func() {
		select {
		case <-q.done:
		case <-ctx.Done():
			q.mu.Lock()
			abort := q.pending.Load() > 0
			if abort {
				q.abortErr = ctx.Err()
				q.cancel()
			}
			q.mu.Unlock()
			if abort {
				for group := range q.groups {
					q.groups[group].transport.close()
				}
			}
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

func (q *Queue) finish() {
	q.sending.Wait()
	// 四组退出意味着接收已统一截止，不会再登记同步发送；截止期限仍可中断在途调用。
	q.syncSending.Wait()
	q.cancel()
	close(q.done)
}
