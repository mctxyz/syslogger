package textfile

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/mctxyz/syslogger/internal/identity"
)

func TestWriteReplacementAndCleanup(t *testing.T) {
	dir := t.TempDir()
	filename := Filename(identity.Instance{Project: "demo"})
	foreign := filepath.Join(dir, ".other.tmp-owned-by-someone-else")
	if err := os.WriteFile(foreign, []byte("保留"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{"old\n", "new\n"} {
		if err := Write(dir, filename, []byte(data)); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(dir, filename))
		if err != nil || string(got) != data {
			t.Fatalf("交付内容不符: %q, %v", got, err)
		}
	}
	info, err := os.Stat(filepath.Join(dir, filename))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0644 {
		t.Fatalf("文件权限不符: %v", info.Mode())
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("临时资源残留或其他文件被删除: %+v, %v", entries, err)
	}
	if data, err := os.ReadFile(foreign); err != nil || string(data) != "保留" {
		t.Fatalf("其他写入方临时文件被改变: %q, %v", data, err)
	}
}

func TestWriteFailuresAndRetry(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing")
	if err := Write(missing, "state.prom", []byte("first")); err == nil {
		t.Fatal("缺失目录的写入应失败")
	}
	if err := os.Mkdir(missing, 0700); err != nil {
		t.Fatal(err)
	}
	if err := Write(missing, "state.prom", []byte("retry")); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(dir, "blocked.prom")
	if err := os.Mkdir(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(blocked, "marker")
	if err := os.WriteFile(marker, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Write(dir, "blocked.prom", []byte("new")); err == nil {
		t.Fatal("目录目标的替换应失败")
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "old" {
		t.Fatalf("失败改变了既有目标: %q, %v", data, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("失败后临时文件未清理: %+v, %v", entries, err)
	}
}

func TestAtomicVisibilityOnLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("同目录原子替换以 Linux 本地文件系统验收")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "state.prom")
	old := bytes.Repeat([]byte("old\n"), 2048)
	next := bytes.Repeat([]byte("new\n"), 4096)
	if err := Write(dir, "state.prom", old); err != nil {
		t.Fatal(err)
	}
	opened, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	stop := make(chan struct{})
	failures := make(chan error, 1)
	var reader sync.WaitGroup
	reader.Add(1)
	go func() {
		defer reader.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			data, err := os.ReadFile(path)
			if err != nil {
				failures <- err
				return
			}
			if !bytes.Equal(data, old) && !bytes.Equal(data, next) {
				failures <- errors.New("原子替换期间读取到了不完整快照")
				return
			}
		}
	}()
	for i := 0; i < 30; i++ {
		data := old
		if i%2 == 0 {
			data = next
		}
		if err := Write(dir, "state.prom", data); err != nil {
			close(stop)
			reader.Wait()
			t.Fatal(err)
		}
	}
	close(stop)
	reader.Wait()
	select {
	case err := <-failures:
		t.Fatal(err)
	default:
	}
	if err := Write(dir, "state.prom", next); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(opened)
	if err != nil || !bytes.Equal(data, old) {
		t.Fatalf("已打开的描述符未保留完整旧内容: %v", err)
	}
	if data, err := os.ReadFile(path); err != nil || !bytes.Equal(data, next) {
		t.Fatalf("重新打开路径未读取到完整新内容: %v", err)
	}
}
