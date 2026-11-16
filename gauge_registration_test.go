package syslogger

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// describingHealthCollector 在真实注册描述过程中回报健康，覆盖共享注册表的回调锁序。
type describingHealthCollector struct {
	component *Component
	desc      *prometheus.Desc
	entered   chan struct{}
	release   chan struct{}
	once      sync.Once
}

// Describe 使用官方采集式描述，使健康回调发生在 Registry 的注册流程中。
func (c *describingHealthCollector) Describe(ch chan<- *prometheus.Desc) {
	prometheus.DescribeByCollect(c, ch)
}

// Collect 首次通过足量描述符交接确认 Registry 已取得锁，再等待测试放行业务回调。
func (c *describingHealthCollector) Collect(ch chan<- prometheus.Metric) {
	metric := prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, 1)
	c.once.Do(func() {
		// 锁定依赖 v1.18.0 的描述通道容量为 10；重复描述符按官方注册规则合并。
		for sample := 0; sample < 32; sample++ {
			ch <- metric
		}
		close(c.entered)
		<-c.release
	})
	c.component.Healthy("registry_probe")
	ch <- metric
}

// gaugeRegistrationInRegistry 用真实协程栈确认注册已进入官方表，避免睡眠猜测或生产测试钩子。
func gaugeRegistrationInRegistry() bool {
	count, _ := runtime.GoroutineProfile(nil)
	stacks := make([]runtime.StackRecord, count+16)
	count, ok := runtime.GoroutineProfile(stacks)
	if !ok {
		return false
	}
	for _, stack := range stacks[:count] {
		frames := runtime.CallersFrames(stack.Stack())
		gauge, registry := false, false
		for {
			frame, more := frames.Next()
			switch frame.Function {
			case "github.com/mctxyz/syslogger.(*Component).RegisterGauge":
				gauge = true
			case "github.com/prometheus/client_golang/prometheus.(*Registry).Register":
				registry = true
			}
			if gauge && registry {
				return true
			}
			if !more {
				break
			}
		}
	}
	return false
}

func TestGaugeRegistrationDuringBusinessDescribe(t *testing.T) {
	const child = "SYSLOGGER_GAUGE_REGISTRATION_CHILD"
	if os.Getenv(child) != "1" {
		// 死锁回归放在子进程，超时后仍能回收整个测试及其临时文件。
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestGaugeRegistrationDuringBusinessDescribe$", "-test.timeout=10s")
		command.Env = append(os.Environ(), child+"=1", "TMPDIR="+t.TempDir())
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("共享注册表并发注册失败: %v\n%s", err, output)
		}
		return
	}
	for _, mode := range []string{"health", "freeze", "close"} {
		t.Run(mode, func(t *testing.T) {
			cfg := testConfig(t)
			cfg.HTTP.Enabled = true
			m := testManager(t, cfg)
			component := testComponent(t, m, "worker")
			business := &describingHealthCollector{
				component: component,
				desc:      prometheus.NewDesc("business_probe", "业务采集状态", nil, nil),
				entered:   make(chan struct{}),
				release:   make(chan struct{}),
			}
			externalDone := make(chan error, 1)
			go func() {
				externalDone <- m.Registry().Register(business)
			}()
			<-business.entered
			definition := GaugeConfig{Metric: "quantity", Name: "pending", Help: "当前数量"}
			type registration struct {
				gauge *Gauge
				err   error
			}
			registered := make(chan registration, 1)
			go func() {
				gauge, err := component.RegisterGauge(definition)
				registered <- registration{gauge: gauge, err: err}
			}()
			waitUntil(t, gaugeRegistrationInRegistry)
			var closed chan error
			switch mode {
			case "freeze":
				if _, err := m.Handler(); err != nil {
					t.Fatal(err)
				}
			case "close":
				closed = make(chan error, 1)
				go func() {
					closed <- m.Close(context.Background())
				}()
				waitUntil(t, func() bool {
					m.mu.RLock()
					defer m.mu.RUnlock()
					return !m.closedAt.IsZero()
				})
				select {
				case err := <-closed:
					t.Fatalf("注册尚未完成时 Close 已结束: %v", err)
				default:
				}
			}
			close(business.release)
			if err := <-externalDone; err != nil {
				t.Fatal(err)
			}
			result := <-registered
			if mode == "close" {
				if !errors.Is(result.err, ErrClosed) || result.gauge != nil {
					t.Fatalf("关闭期间注册未拒收: gauge=%v err=%v", result.gauge, result.err)
				}
				if err := <-closed; err != nil {
					t.Fatal(err)
				}
			} else {
				if result.err != nil {
					t.Fatalf("冻结前已获准的注册失败: %v", result.err)
				}
				if err := result.gauge.Set(5); err != nil {
					t.Fatal(err)
				}
				if mode == "freeze" {
					if _, err := component.RegisterGauge(GaugeConfig{Metric: "late", Name: "late"}); err == nil {
						t.Fatal("冻结后仍允许新注册准入")
					}
				}
				if err := m.Close(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			// 未 Set 的 GaugeVec 没有采集样本，重注册同描述符才能证明关闭时已释放。
			replacement := prometheus.NewGaugeVec(prometheus.GaugeOpts{
				Namespace: "syslogger",
				Subsystem: "component",
				Name:      definition.Metric,
				Help:      definition.Help,
				ConstLabels: prometheus.Labels{
					"project": cfg.Project, "instance_id": cfg.InstanceID, "component": "worker", "name": definition.Name,
				},
			}, nil)
			if err := m.Registry().Register(replacement); err != nil {
				t.Fatalf("关闭遗漏了注册资源: %v", err)
			}
			m.Registry().Unregister(replacement)
			families, err := m.Registry().Gather()
			if err != nil || len(families) != 1 || families[0].GetName() != "business_probe" {
				t.Fatalf("关闭影响业务 Collector 或遗漏自有指标: %v", err)
			}
		})
	}
}
