package main

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/mctxyz/syslogger/internal/journalwire"
)

func TestInitOptionsStaySeparate(t *testing.T) {
	opts, err := parseInitOptions(nil, io.Discard)
	if err != nil || opts.JournalMaxUse != [journalwire.GroupCount]string{"1G", "2G", "1G", "1G"} {
		t.Fatalf("初始化默认参数错误: %+v %v", opts, err)
	}
	opts, err = parseInitOptions([]string{"--journal-info-max-use", "10G"}, io.Discard)
	if err != nil || opts.JournalMaxUse != [journalwire.GroupCount]string{"1G", "10G", "1G", "1G"} {
		t.Fatalf("容量参数未生效: %+v %v", opts, err)
	}
	for _, args := range [][]string{{"--colour"}, {"--colour=false"}} {
		opts, err := parseInitOptions(args, io.Discard)
		if err != nil || opts.JournalMaxUse != [journalwire.GroupCount]string{"1G", "2G", "1G", "1G"} {
			t.Fatalf("初始化的颜色修饰项影响了配置：%v %+v %v", args, opts, err)
		}
	}
	for _, args := range [][]string{{"--user", "worker"}, {"--list"}, {"--project", "demo"}, {"--journal-debug-max-use", "0"}, {"--journal-max-use", "5G"}, {"positional"}} {
		if _, err := parseInitOptions(args, io.Discard); err == nil {
			t.Fatalf("初始化接受了无效参数: %v", args)
		}
	}
	for _, args := range [][]string{{"--init"}, {"--user", "worker"}, {"--journal-info-max-use", "10G"}} {
		if _, err := parseOptions(args, io.Discard); err == nil {
			t.Fatalf("初始化参数污染了查询入口: %v", args)
		}
	}
	var help bytes.Buffer
	if _, err := parseInitOptions([]string{"--help"}, &help); !errors.Is(err, flag.ErrHelp) || !strings.Contains(help.String(), "mct-syslogger init") {
		t.Fatalf("独立帮助不可用: %s %v", help.String(), err)
	}
	for _, usage := range []string{"root:mct-syslogger", "sudo usermod -aG mct-syslogger username"} {
		if !strings.Contains(help.String(), usage) {
			t.Fatalf("帮助缺少共享组权限契约: %s", usage)
		}
	}
}

func TestInitDispatchHelp(t *testing.T) {
	previous := os.Args
	t.Cleanup(func() { os.Args = previous })
	for _, args := range [][]string{
		{"mct-syslogger", "init", "--help"},
		{"mct-syslogger", "init", "--colour", "--help"},
		{"mct-syslogger", "init", "--colour=false", "--help"},
	} {
		os.Args = args
		if code := mainCode(); code != 0 {
			t.Fatalf("init 子命令没有被独立分发: %v %d", args, code)
		}
	}
}
