package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/mctxyz/syslogger/internal/catalog"
	"github.com/mctxyz/syslogger/internal/logging"
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
		"--include-level LEVEL",
		"--exclude-level LEVEL",
		"-s, --since TIME",
		"-u, --until TIME",
		"-n, --lines N",
		"-f, --follow",
		"-k, --keyword TEXT",
		"-e, --regex REGEX",
		"--ignore-case",
		"--debug",
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
	for _, group := range optionGroups {
		if !strings.Contains(help.String(), group.title+"：") {
			t.Fatalf("帮助缺少注册的参数分组：%s", group.title)
		}
	}
	for _, usage := range []string{
		"NDJSON", "stdout", "stderr", "(?i)",
		"project", "instance_id", "components[].name",
		"mct-syslogger init --help",
	} {
		if !strings.Contains(help.String(), usage) {
			t.Fatalf("帮助缺少使用说明：%s", usage)
		}
	}
}

func TestHelpExamples(t *testing.T) {
	var help bytes.Buffer
	if _, err := parseOptions([]string{"--help"}, &help); !errors.Is(err, flag.ErrHelp) {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	store := catalog.Store{Root: preparedCatalogRoot(t)}
	instance := catalog.Instance{
		Project:    "example-project",
		InstanceID: "example-instance",
		Components: []catalog.Component{{
			Name:         "example-component",
			CreatedAt:    at,
			LastActiveAt: at,
			LogCounts:    catalogCounts(at, 1),
		}},
	}
	if err := store.Merge(instance, at); err != nil {
		t.Fatal(err)
	}
	entry := moduleJournalEntry(t, "example timeout", instance.Project, instance.InstanceID, instance.Components[0].Name, logging.Error)
	var directoryJSON, logJSON bool
	for _, line := range strings.Split(help.String(), "\n") {
		// 四格缩进标记可直接执行的示例；用法概览和参数片段不作为完整命令运行。
		if !strings.HasPrefix(line, "    mct-syslogger ") {
			continue
		}
		args := strings.Fields(strings.TrimSpace(line))[1:]
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if args[0] == "init" {
				// 仅解析子命令帮助，不执行任何主机初始化操作。
				if _, err := parseInitOptions(args[1:], io.Discard); !errors.Is(err, flag.ErrHelp) {
					t.Fatalf("主机初始化帮助示例无效：%v", err)
				}
				return
			}
			opts := testOptions(t, args...)
			for _, values := range []stringList{opts.projects, opts.instances, opts.components} {
				for _, value := range values {
					if !strings.HasPrefix(value, "example-") {
						t.Fatalf("身份示例应使用通用 example-* 名称：%q", value)
					}
				}
			}
			var output bytes.Buffer
			if opts.list {
				if err := showCatalog(store, opts, &output); err != nil {
					t.Fatal(err)
				}
				if opts.asJSON {
					var instances []catalog.Instance
					if err := json.Unmarshal(output.Bytes(), &instances); err != nil {
						t.Fatal(err)
					}
					if len(instances) != 1 {
						t.Fatalf("目录 JSON 示例未返回测试实例：%s", output.String())
					}
					got := instances[0]
					if got.Project != instance.Project || got.InstanceID != instance.InstanceID {
						t.Fatalf("目录 JSON 缺少查询所需的项目或实例名：%s", output.String())
					}
					if len(got.Components) != 1 || got.Components[0].Name != instance.Components[0].Name {
						t.Fatalf("目录 JSON 缺少查询所需的组件名：%s", output.String())
					}
					directoryJSON = true
				}
			} else {
				fakeJournalEntries(t, []journalTestEntry{entry})
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := query(ctx, opts, &output, io.Discard); err != nil {
					t.Fatal(err)
				}
				if opts.asJSON {
					if output.String() != entry.Message+"\n" {
						t.Fatalf("日志 JSON 示例未保持 NDJSON 输出：%q", output.String())
					}
					logJSON = true
				}
			}
			if output.Len() == 0 {
				t.Fatal("帮助示例未展示测试身份或日志")
			}
		})
	}
	if !directoryJSON || !logJSON {
		t.Fatal("帮助需要同时提供 JSON 身份发现和日志查询示例")
	}
}
