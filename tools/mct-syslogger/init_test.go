package main

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"os"
	"strings"
	"testing"
)

func TestInitOptionsStaySeparate(t *testing.T) {
	opts, err := parseInitOptions([]string{"--user", "appuser"}, io.Discard)
	if err != nil || opts.User != "appuser" || opts.JournalMaxUse != "5G" {
		t.Fatalf("初始化默认参数错误: %+v %v", opts, err)
	}
	opts, err = parseInitOptions([]string{"--user", "appuser", "--journal-max-use", "10G"}, io.Discard)
	if err != nil || opts.JournalMaxUse != "10G" {
		t.Fatalf("容量参数未生效: %+v %v", opts, err)
	}
	for _, args := range [][]string{{}, {"--user", "app", "--list"}, {"--user", "app", "--project", "demo"}, {"--user", "app", "--journal-max-use", "0"}, {"--user", "app", "positional"}} {
		if _, err := parseInitOptions(args, io.Discard); err == nil {
			t.Fatalf("初始化接受了无效参数: %v", args)
		}
	}
	for _, args := range [][]string{{"--init"}, {"--user", "app"}, {"--journal-max-use", "10G"}} {
		if _, err := parseOptions(args, io.Discard); err == nil {
			t.Fatalf("初始化参数污染了查询入口: %v", args)
		}
	}
	var help bytes.Buffer
	if _, err := parseInitOptions([]string{"--help"}, &help); !errors.Is(err, flag.ErrHelp) || !strings.Contains(help.String(), "mct-syslogger init") {
		t.Fatalf("独立帮助不可用: %s %v", help.String(), err)
	}
}

func TestInitDispatchHelp(t *testing.T) {
	previous := os.Args
	t.Cleanup(func() { os.Args = previous })
	os.Args = []string{"mct-syslogger", "init", "--help"}
	if code := mainCode(); code != 0 {
		t.Fatalf("init 子命令没有被独立分发: %d", code)
	}
}
