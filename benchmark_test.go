package syslogger

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func BenchmarkLogDelivery(b *testing.B) {
	for _, size := range []int{512, 4096} {
		for _, capacity := range []int{1, 512, 4096} {
			for _, modules := range []int{1, 4} {
				for _, parallel := range []bool{false, true} {
					b.Run(fmt.Sprintf("message_%d/capacity_%d/modules_%d/parallel_%v", size, capacity, modules, parallel), func(b *testing.B) {
						path := filepath.Join(socketDirectory(b), "log.sock")
						receiver := unixReceiver(b, path)
						var received atomic.Uint64
						readerDone := make(chan struct{})
						go func() {
							defer close(readerDone)
							data := make([]byte, 65536)
							for {
								if _, _, err := receiver.ReadFromUnix(data); err != nil {
									return
								}
								received.Add(1)
							}
						}()
						defer func() { _ = receiver.Close(); <-readerDone }()
						manager, err := New(Config{Project: "bench", SyslogSocketPath: path})
						if err != nil {
							b.Fatal(err)
						}
						defer manager.Close(context.Background())
						components := make([]*Component, modules)
						for i := range components {
							components[i], err = manager.Register(ComponentConfig{Name: fmt.Sprint(i), QueueCapacity: capacity})
							if err != nil {
								b.Fatal(err)
							}
						}
						message := strings.Repeat("x", size)
						var sequence atomic.Uint64
						submit := func() {
							n := sequence.Add(1)
							components[n%uint64(modules)].Info(message, nil)
						}
						b.ReportAllocs()
						b.ResetTimer()
						started := time.Now()
						if parallel {
							b.RunParallel(func(pb *testing.PB) {
								for pb.Next() {
									submit()
								}
							})
						} else {
							for n := 0; n < b.N; n++ {
								submit()
							}
						}
						b.StopTimer()
						if err := manager.Close(context.Background()); err != nil {
							b.Fatal(err)
						}
						// 提交接口通过模块 Counter 保留拒收事实，排空后统一核对交付数量。
						var refused uint64
						for _, component := range components {
							rejected, failed := component.logs.Counts()
							if failed != 0 {
								b.Fatalf("模块发生后台发送失败: %d", failed)
							}
							refused += rejected
						}
						accepted := uint64(b.N) - refused
						deadline := time.Now().Add(5 * time.Second)
						for received.Load() < accepted {
							if time.Now().After(deadline) {
								b.Fatal("接收条数与成功提交不符")
							}
							time.Sleep(time.Millisecond)
						}
						if received.Load() != accepted {
							b.Fatal("接收条数与成功提交不符")
						}
						b.ReportMetric(float64(accepted)/float64(b.N), "accepted/op")
						b.ReportMetric(float64(refused)/float64(b.N), "rejected/op")
						b.ReportMetric(float64(received.Load())/time.Since(started).Seconds(), "received/s")
					})
				}
			}
		}
	}
}
