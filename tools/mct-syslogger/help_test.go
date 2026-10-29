package main

import (
	"bytes"
	"errors"
	"flag"
	"regexp"
	"strings"
	"testing"
)

func TestHelpLayout(t *testing.T) {
	var help bytes.Buffer
	for _, name := range []string{"-h", "--help"} {
		var output bytes.Buffer
		if _, err := parseOptions([]string{name}, &output); !errors.Is(err, flag.ErrHelp) {
			t.Fatalf("帮助命令 %s 失败：%v", name, err)
		}
		if help.Len() > 0 && output.String() != help.String() {
			t.Fatal("长短帮助参数输出不一致")
		}
		help = output
	}
	var rows []string
	for _, line := range strings.Split(help.String(), "\n") {
		// 参数行固定缩进两格，避免将规则中的参数引用和示例命令计入参数表。
		if strings.HasPrefix(line, "  -") {
			rows = append(rows, line[2:])
		}
	}
	want := []string{
		"--list",
		"-p, --project PROJECT",
		"-i, --instance-id INSTANCE",
		"-c, --component COMPONENT",
		"-l, --level LEVEL",
		"--exclude-level LEVEL",
		"-s, --since TIME",
		"-u, --until TIME",
		"-n, --lines N",
		"-f, --follow",
		"-k, --keyword TEXT",
		"-e, --regex REGEX",
		"--ignore-case",
		"--json",
		"--colour",
		"-h, --help",
	}
	if len(rows) != len(want) {
		t.Fatalf("每个功能应只显示一行帮助，实际为 %d 行：\n%s", len(rows), help.String())
	}
	separator := regexp.MustCompile(` {2,}`)
	descriptionColumn := -1
	for index, label := range want {
		parts := separator.Split(rows[index], 2)
		if len(parts) != 2 || parts[0] != label || parts[1] == "" {
			t.Fatalf("第 %d 行参数、值占位符或说明不符：%q", index+1, rows[index])
		}
		column := strings.Index(rows[index], parts[1])
		if descriptionColumn >= 0 && column != descriptionColumn {
			t.Fatalf("参数说明未对齐：%q", rows[index])
		}
		descriptionColumn = column
	}
	if strings.ContainsAny(help.String(), "\t\x1b") {
		t.Fatal("帮助输出应为已对齐的纯文本")
	}
	for _, section := range []string{
		"查询方式：",
		"身份筛选（日志 / 目录通用）：",
		"内容搜索（仅日志查询）：",
		"参数与组合规则：",
		"时间与条数：",
		"输出与退出码：",
		"排障示例",
		"无结果与权限排查：",
	} {
		if !strings.Contains(help.String(), section) {
			t.Fatalf("帮助缺少分组：%s", section)
		}
	}
	for _, usage := range []string{
		"NDJSON", "stdout", "stderr", "(?i)",
		"sudo usermod -aG systemd-journal username",
		"退出并重新登录后生效",
	} {
		if !strings.Contains(help.String(), usage) {
			t.Fatalf("帮助缺少使用说明：%s", usage)
		}
	}
}
