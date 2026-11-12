package logging

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mctxyz/syslogger/internal/journalwire"
)

func BenchmarkTransportSend(b *testing.B) {
	for _, protocol := range []wireProtocol{journalNative, syslogDatagram, syslogStream} {
		for _, size := range []int{512, 4096} {
			name := [...]string{"syslog_datagram", "syslog_stream", "journal"}[protocol]
			b.Run(fmt.Sprintf("%s/%d", name, size), func(b *testing.B) {
				if protocol == journalNative && runtime.GOOS != "linux" {
					b.Skip("原生 journal 发送需要 Linux")
				}
				tr, receiver := connectedTransport(b, protocol)
				entry := &Entry{Severity: Info, Body: []byte(strings.Repeat("x", size))}
				frameSize := len(journalwire.AppendMessage(nil, tr.journalHeader, entry.Body, uint8(Info)))
				if protocol != journalNative {
					frameSize = len(fmt.Sprintf("<14>%s bench[%d]: ", time.Now().Format(time.Stamp), os.Getpid())) + size
					if protocol == syslogStream {
						frameSize++
					}
				}
				data := make([]byte, frameSize)
				var received atomic.Int64
				readErr := make(chan error, 1)
				done := make(chan struct{})
				go func() {
					defer close(done)
					for {
						var n int
						var err error
						if protocol == syslogStream {
							n, err = io.ReadFull(receiver, data)
						} else {
							n, err = receiver.Read(data)
						}
						if err != nil {
							readErr <- err
							return
						}
						if n != frameSize {
							readErr <- fmt.Errorf("协议消息长度不符: got=%d want=%d", n, frameSize)
							return
						}
						received.Add(1)
					}
				}()
				defer func() {
					_ = receiver.Close()
					<-done
				}()
				b.ReportAllocs()
				b.ResetTimer()
				var buffer []byte
				for n := 0; n < b.N; n++ {
					var err error
					buffer, err = tr.send(context.Background(), entry, buffer)
					if err != nil {
						b.Fatal(err)
					}
				}
				// 接收排空也计入时间和分配，成功数必须与全部发送尝试一致。
				deadline := time.Now().Add(5 * time.Second)
				for received.Load() != int64(b.N) {
					select {
					case err := <-readErr:
						b.Fatal(err)
					default:
					}
					if time.Now().After(deadline) {
						b.Fatalf("接收未完成: got=%d want=%d", received.Load(), b.N)
					}
					runtime.Gosched()
				}
				elapsed := b.Elapsed().Seconds()
				b.StopTimer()
				b.ReportMetric(float64(received.Load())/elapsed, "received/s")
			})
		}
	}
}
