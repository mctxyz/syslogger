package syslogger

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mctxyz/syslogger/internal/record"
)

func testConfig(t *testing.T) Config {
	t.Helper()
	return Config{Project: "demo", InstanceID: "instance-a", StateEnabled: true, SyslogSocketPath: filepath.Join(t.TempDir(), "missing.sock"), File: FileConfig{OutputPath: filepath.Join(t.TempDir(), "metrics.prom"), FlushInterval: time.Hour}}
}

func testManager(t *testing.T, cfg Config) *Manager {
	t.Helper()
	m, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = m.Close(ctx)
	})
	return m
}

func testComponent(t *testing.T, m *Manager, name string) *Component {
	t.Helper()
	c, err := m.Register(name, 0)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func currentRecord(t *testing.T, m *Manager, name string) record.Record {
	t.Helper()
	for _, item := range m.snapshot() {
		if item.Name == name {
			return item.Record
		}
	}
	t.Fatalf("缺失组件 %q", name)
	return record.Record{}
}

func waitUntil(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("等待后台处理超时")
		}
		time.Sleep(time.Millisecond)
	}
}

func readSnapshot(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func socketDirectory(t testing.TB) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "slg-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func unixReceiver(t testing.TB, path string) *net.UnixConn {
	t.Helper()
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func receive(t *testing.T, conn *net.UnixConn) []byte {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	data := make([]byte, 65536)
	n, _, err := conn.ReadFromUnix(data)
	if err != nil {
		t.Fatal(err)
	}
	return data[:n]
}
