package syslogger

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFileUsesConfiguredPath(t *testing.T) {
	for _, test := range []struct {
		name, filename string
		relative       bool
	}{
		{"custom", "rpc-status.prom", false},
		{"unicode", "指标 status.prom", false},
		{"extension", "metrics.txt", false},
		{"relative", "relative.prom", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := testConfig(t)
			cfg.File.Enabled = true
			path := filepath.Join(filepath.Dir(cfg.File.OutputPath), test.filename)
			cfg.File.OutputPath = path
			if test.relative {
				cwd, err := os.Getwd()
				if err != nil {
					t.Fatal(err)
				}
				cfg.File.OutputPath, err = filepath.Rel(cwd, path)
				if err != nil {
					t.Fatal(err)
				}
			}
			m := testManager(t, cfg)
			testComponent(t, m, "rpc").Healthy()
			if err := m.StartFile(); err != nil {
				t.Fatal(err)
			}
			if err := m.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(readSnapshot(t, path)) == 0 {
				t.Fatal("配置的目标文件为空")
			}
			entries, err := os.ReadDir(filepath.Dir(path))
			if err != nil || len(entries) != 1 || entries[0].Name() != test.filename {
				t.Fatalf("目标文件名被改变或存在临时文件残留: %v, %v", entries, err)
			}
		})
	}
}
