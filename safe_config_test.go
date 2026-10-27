package syslogger

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mctxyz/syslogger/internal/logging"
)

func assertCorrections(t *testing.T, cfg Config, corrections []configCorrection, want map[string]logging.Level) {
	t.Helper()
	normalized, err := cfg.normalized()
	if err != nil {
		t.Fatalf("纠正后的配置仍不合法：%v", err)
	}
	if !reflect.DeepEqual(normalized, cfg) {
		t.Fatalf("纠正后的配置尚有未补齐的默认值：%+v", cfg)
	}
	if len(corrections) != len(want) {
		t.Fatalf("诊断条数不符：got=%+v want=%v", corrections, want)
	}
	seen := make(map[string]bool)
	for _, correction := range corrections {
		level, exists := want[correction.field]
		if !exists || level != correction.level || seen[correction.field] {
			t.Fatalf("字段诊断不符或重复：%+v", correction)
		}
		if correction.reason == "" {
			t.Fatal("字段诊断缺少原因")
		}
		seen[correction.field] = true
	}
}

func TestSafeConfigProject(t *testing.T) {
	for _, test := range []struct {
		value string
		want  string
	}{
		{
			value: "",
			want:  "common",
		},
		{
			value: " \t ",
			want:  "unknown",
		},
		{
			value: "bad\xff",
			want:  "unknown",
		},
		{
			value: strings.Repeat("p", 22),
			want:  "unknown",
		},
		{
			value: " 项目 A ",
			want:  " 项目 A ",
		},
	} {
		t.Run(test.value, func(t *testing.T) {
			cfg, corrections := (Config{
				Project:            test.value,
				MaxLabelValueBytes: 21,
			}).corrected()
			if cfg.Project != test.want || cfg.InstanceID != "common" {
				t.Fatalf("项目身份回退不符：%+v", cfg)
			}
			want := make(map[string]logging.Level)
			if test.value != test.want {
				want["Project"] = logging.Warning
			}
			assertCorrections(t, cfg, corrections, want)
		})
	}
}

func TestSafeConfigInstance(t *testing.T) {
	for _, test := range []struct {
		value string
		limit int
		want  string
	}{
		{
			value: "",
			want:  "common",
		},
		{
			value: "ab",
			want:  "common",
		},
		{
			value: "bad\xff",
			want:  "common",
		},
		{
			value: strings.Repeat("i", 129),
			want:  "common",
		},
		{
			value: strings.Repeat("i", 22),
			limit: 21,
			want:  "common",
		},
		{
			value: strings.Repeat("i", 128),
			want:  strings.Repeat("i", 128),
		},
		{
			value: " \t ",
			want:  " \t ",
		},
		{
			value: "../中文/ID",
			want:  "../中文/ID",
		},
	} {
		t.Run(test.value, func(t *testing.T) {
			cfg, corrections := (Config{
				Project:            "demo",
				InstanceID:         test.value,
				MaxLabelValueBytes: test.limit,
			}).corrected()
			if cfg.InstanceID != test.want {
				t.Fatalf("实例身份回退不符：%q", cfg.InstanceID)
			}
			want := make(map[string]logging.Level)
			if test.value != "" && test.value != test.want {
				want["InstanceID"] = logging.Warning
			}
			assertCorrections(t, cfg, corrections, want)
		})
	}
}

func TestSafeConfigNumericValues(t *testing.T) {
	for _, limit := range []int{-1, 0, 1, 20, 21, 2048} {
		cfg, corrections := (Config{
			Project:            "demo",
			MaxLabelValueBytes: limit,
		}).corrected()
		want := make(map[string]logging.Level)
		if limit != 0 && limit < 21 {
			want["MaxLabelValueBytes"] = logging.Warning
		}
		if limit < 21 && cfg.MaxLabelValueBytes != 2048 {
			t.Fatalf("标签上限未回退默认值：%d", cfg.MaxLabelValueBytes)
		}
		assertCorrections(t, cfg, corrections, want)
	}
	for _, interval := range []time.Duration{-time.Second, 0, time.Second} {
		cfg, corrections := (Config{
			Project:      "demo",
			StateEnabled: true,
			File: FileConfig{
				Enabled:       true,
				OutputPath:    "metrics.prom",
				FlushInterval: interval,
			},
		}).corrected()
		want := make(map[string]logging.Level)
		if interval < 0 {
			want["File.FlushInterval"] = logging.Warning
		}
		if interval <= 0 && cfg.File.FlushInterval != 10*time.Second {
			t.Fatalf("刷新间隔未使用默认值：%s", cfg.File.FlushInterval)
		}
		assertCorrections(t, cfg, corrections, want)
	}
	idle := FileConfig{
		OutputPath:    " \t ",
		FlushInterval: -time.Second,
	}
	cfg, corrections := (Config{
		Project: "demo",
		File:    idle,
	}).corrected()
	if cfg.File != idle {
		t.Fatal("未启用的指标文件配置被改变")
	}
	assertCorrections(t, cfg, corrections, nil)
}

func TestSafeConfigOutlets(t *testing.T) {
	for _, test := range []struct {
		name        string
		fileEnabled bool
		path        string
		httpEnabled bool
		wantFile    bool
		wantState   bool
		fields      map[string]logging.Level
	}{
		{
			name: "disabled",
		},
		{
			name:        "http",
			httpEnabled: true,
			wantState:   true,
			fields: map[string]logging.Level{
				"StateEnabled": logging.Warning,
			},
		},
		{
			name:        "file",
			fileEnabled: true,
			path:        "metrics.prom",
			wantFile:    true,
			wantState:   true,
			fields: map[string]logging.Level{
				"StateEnabled": logging.Warning,
			},
		},
		{
			name:        "empty_path",
			fileEnabled: true,
			fields: map[string]logging.Level{
				"File.Enabled": logging.Error,
			},
		},
		{
			name:        "blank_path_with_http",
			fileEnabled: true,
			path:        " \t ",
			httpEnabled: true,
			wantState:   true,
			fields: map[string]logging.Level{
				"File.Enabled": logging.Error,
				"StateEnabled": logging.Warning,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg, corrections := (Config{
				Project: "demo",
				HTTP: HTTPConfig{
					Enabled: test.httpEnabled,
				},
				File: FileConfig{
					Enabled:    test.fileEnabled,
					OutputPath: test.path,
				},
			}).corrected()
			if cfg.File.Enabled != test.wantFile || cfg.StateEnabled != test.wantState {
				t.Fatalf("指标出口及状态依赖不符：%+v", cfg)
			}
			if cfg.HTTP.Enabled != test.httpEnabled || cfg.HTTP.ListenAddr != "" {
				t.Fatal("HTTP 出口配置被额外改变")
			}
			assertCorrections(t, cfg, corrections, test.fields)
		})
	}
	cfg, corrections := (Config{
		Project:      "demo",
		StateEnabled: true,
		File: FileConfig{
			Enabled: true,
		},
	}).corrected()
	if !cfg.StateEnabled || cfg.File.Enabled {
		t.Fatal("停用指标文件时改变了原本已启用的状态维护")
	}
	assertCorrections(t, cfg, corrections, map[string]logging.Level{
		"File.Enabled": logging.Error,
	})
}

func TestSafeQueueHealthConfig(t *testing.T) {
	for _, interval := range []time.Duration{-time.Second, 0, time.Minute} {
		cfg, corrections := (Config{
			Project:                   "demo",
			DegradeOnQueueFull:        true,
			QueueFullRecoveryInterval: interval,
		}).corrected()
		want := map[string]logging.Level{
			"StateEnabled": logging.Warning,
		}
		if interval < 0 {
			want["QueueFullRecoveryInterval"] = logging.Warning
		}
		if !cfg.StateEnabled || !cfg.DegradeOnQueueFull {
			t.Fatal("队满自动健康的状态维护依赖未补齐")
		}
		if interval <= 0 && cfg.QueueFullRecoveryInterval != 30*time.Second {
			t.Fatal("队满恢复窗口未使用默认值")
		}
		if interval > 0 && cfg.QueueFullRecoveryInterval != interval {
			t.Fatal("合法恢复窗口被改变")
		}
		assertCorrections(t, cfg, corrections, want)
	}
}

// assertSafeConfigProperties 核对严格校验、合法配置一致性和重复纠正的幂等性。
func assertSafeConfigProperties(t *testing.T, input Config) {
	t.Helper()
	cfg, corrections := input.corrected()
	normalized, err := cfg.normalized()
	if err != nil || !reflect.DeepEqual(normalized, cfg) {
		t.Fatalf("纠正后的配置未通过严格校验：cfg=%+v err=%v", cfg, err)
	}
	if valid, err := input.normalized(); err == nil {
		if len(corrections) != 0 || !reflect.DeepEqual(cfg, valid) {
			t.Fatal("严格构造接受的配置被额外纠正")
		}
	}
	second, repeated := cfg.corrected()
	if len(repeated) != 0 || !reflect.DeepEqual(second, cfg) {
		t.Fatal("配置纠正不具备幂等性")
	}
}

func FuzzSafeConfig(f *testing.F) {
	f.Add(
		"",
		"",
		-1,
		int64(-1),
		true,
		true,
		false,
		" ",
	)
	f.Add(
		"项目",
		"instance-a",
		0,
		int64(0),
		false,
		true,
		true,
		"metrics.prom",
	)
	f.Add(
		"bad\xff",
		"a",
		21,
		int64(1),
		false,
		false,
		false,
		"",
	)
	f.Fuzz(func(
		t *testing.T,
		project string,
		instance string,
		limit int,
		interval int64,
		httpEnabled bool,
		fileEnabled bool,
		stateEnabled bool,
		path string,
	) {
		input := Config{
			Project:            project,
			InstanceID:         instance,
			MaxLabelValueBytes: limit,
			StateEnabled:       stateEnabled,
			HTTP: HTTPConfig{
				Enabled: httpEnabled,
			},
			File: FileConfig{
				Enabled:       fileEnabled,
				OutputPath:    path,
				FlushInterval: time.Duration(interval),
			},
		}
		assertSafeConfigProperties(t, input)
	})
}

func FuzzQueueHealthConfig(f *testing.F) {
	f.Add(true, false, int64(-1))
	f.Add(true, true, int64(0))
	f.Add(false, false, int64(-1))
	f.Fuzz(func(t *testing.T, enabled bool, stateEnabled bool, interval int64) {
		assertSafeConfigProperties(t, Config{
			Project:                   "demo",
			StateEnabled:              stateEnabled,
			DegradeOnQueueFull:        enabled,
			QueueFullRecoveryInterval: time.Duration(interval),
		})
	})
}
