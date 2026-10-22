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
						var received, accepted, refused atomic.Uint64
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
							components[i], err = manager.Register(fmt.Sprint(i), capacity)
							if err != nil {
								b.Fatal(err)
							}
						}
						message := strings.Repeat("x", size)
						var sequence atomic.Uint64
						submit := func() {
							n := sequence.Add(1)
							if err := components[n%uint64(modules)].Info(message, nil); err != nil {
								refused.Add(1)
							} else {
								accepted.Add(1)
							}
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
						deadline := time.Now().Add(5 * time.Second)
						for received.Load() < accepted.Load() {
							if time.Now().After(deadline) {
								b.Fatal("接收条数与成功提交不符")
							}
							time.Sleep(time.Millisecond)
						}
						b.ReportMetric(float64(accepted.Load())/float64(b.N), "accepted/op")
						b.ReportMetric(float64(refused.Load())/float64(b.N), "rejected/op")
						b.ReportMetric(float64(received.Load())/time.Since(started).Seconds(), "received/s")
					})
				}
			}
		}
	}
}
