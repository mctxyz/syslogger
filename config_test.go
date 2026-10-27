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
	if m.cfg.DefaultQueueCapacity != 512 {
		t.Fatalf("默认日志队列容量不符: %d", m.cfg.DefaultQueueCapacity)
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
		func(c *Config) { c.File.Enabled = true; c.File.OutputPath = " " },
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
	cfg.File.OutputPath = ""
	cfg.File.FlushInterval = -1
	testManager(t, cfg)
}

func TestQueueHealthConfig(t *testing.T) {
	for _, test := range []struct {
		name     string
		enabled  bool
		state    bool
		interval time.Duration
		want     time.Duration
		invalid  bool
	}{
		{
			name: "disabled",
		},
		{
			name:     "disabled_interval",
			interval: -time.Second,
			want:     -time.Second,
		},
		{
			name:    "default_interval",
			enabled: true,
			state:   true,
			want:    30 * time.Second,
		},
		{
			name:     "custom_interval",
			enabled:  true,
			state:    true,
			interval: time.Minute,
			want:     time.Minute,
		},
		{
			name:    "missing_state",
			enabled: true,
			invalid: true,
		},
		{
			name:     "negative_interval",
			enabled:  true,
			state:    true,
			interval: -time.Second,
			invalid:  true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := Config{
				Project:                   "demo",
				StateEnabled:              test.state,
				DegradeOnQueueFull:        test.enabled,
				QueueFullRecoveryInterval: test.interval,
			}
			if test.invalid {
				if manager, err := New(cfg); manager != nil || err == nil {
					t.Fatalf("非法自动健康配置未拒绝: %+v", cfg)
				}
				return
			}
			manager := testManager(t, cfg)
			if manager.cfg.QueueFullRecoveryInterval != test.want {
				t.Fatalf("恢复窗口不符: got=%s want=%s", manager.cfg.QueueFullRecoveryInterval, test.want)
			}
			if manager.cfg.DegradeOnQueueFull != test.enabled || manager.cfg.StateEnabled != test.state {
				t.Fatal("自动健康配置的启用状态被改变")
			}
		})
	}
}
