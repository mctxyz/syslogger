package syslogger

import (
	"strings"
	"testing"
	"time"
)

func TestConfigDefaultsAndIdentity(t *testing.T) {
	m := testManager(t, Config{Project: " 项目 A "})
	if m.cfg.InstanceID != "common" || m.cfg.Project != " 项目 A " || m.cfg.MaxLabelValueBytes != 2048 || m.cfg.StateEnabled || m.cfg.HTTP.Enabled || m.cfg.File.Enabled {
		t.Fatalf("默认配置不符: %+v", m.cfg)
	}
	for _, id := range []string{"abc", strings.Repeat("a", 128), " \t ", "../中文/ID"} {
		cfg := testConfig(t)
		cfg.InstanceID = id
		m := testManager(t, cfg)
		if m.cfg.InstanceID != id {
			t.Fatalf("原始实例身份被改变: %q", m.cfg.InstanceID)
		}
	}
}

func TestConfigValidationAndOutletCombinations(t *testing.T) {
	for _, change := range []func(*Config){
		func(c *Config) { c.Project = "" }, func(c *Config) { c.Project = "\u3000\t" }, func(c *Config) { c.Project = "\xff" },
		func(c *Config) { c.InstanceID = "ab" }, func(c *Config) { c.InstanceID = strings.Repeat("a", 129) }, func(c *Config) { c.InstanceID = "a\xffb" },
		func(c *Config) { c.MaxLabelValueBytes = 20 }, func(c *Config) { c.MaxLabelValueBytes = -1 },
		func(c *Config) { c.StateEnabled = false; c.HTTP.Enabled = true },
		func(c *Config) { c.StateEnabled = false; c.File.Enabled = true },
		func(c *Config) { c.File.Enabled = true; c.File.OutputDir = " " },
		func(c *Config) { c.File.Enabled = true; c.File.FlushInterval = -1 },
	} {
		cfg := testConfig(t)
		change(&cfg)
		if m, err := New(cfg); m != nil || err == nil {
			t.Fatalf("非法配置未被拒绝: %+v", cfg)
		}
	}
	for flags := 0; flags < 4; flags++ {
		cfg := testConfig(t)
		cfg.HTTP.Enabled = flags&1 != 0
		cfg.File.Enabled = flags&2 != 0
		cfg.File.FlushInterval = 0
		m := testManager(t, cfg)
		if cfg.File.Enabled && m.cfg.File.FlushInterval != 10*time.Second {
			t.Fatal("文件周期默认值不符")
		}
	}
	cfg := testConfig(t)
	cfg.File.OutputDir = ""
	cfg.File.FlushInterval = -1
	testManager(t, cfg)
}
