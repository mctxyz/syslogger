package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"regexp"
	"strings"
	"text/tabwriter"
)

type stringList []string

// String 提供 flag 帮助信息中的默认值表示。
func (values *stringList) String() string {
	return strings.Join(*values, ",")
}

// Set 累积重复参数，保留身份与关键词的原文。
func (values *stringList) Set(value string) error {
	*values = append(*values, value)
	return nil
}

// options 保存命令行条件，正则在启动 journalctl 前统一编译。
type options struct {
	projects          stringList
	instances         stringList
	components        stringList
	levels            stringList
	keywords          stringList
	patterns          []*regexp.Regexp
	since             string
	until             string
	lines             int
	follow            bool
	asJSON            bool
	ignoreKeywordCase bool
	colour            bool
	list              bool
}

func parseOptions(args []string, help io.Writer) (options, error) {
	var opts options
	var patterns stringList
	flags := flag.NewFlagSet("mct-syslogger", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Var(&opts.projects, "project", "精确匹配 project，可重复")
	flags.Var(&opts.instances, "instance-id", "精确匹配 instance_id，可重复")
	flags.Var(&opts.components, "component", "精确匹配 component，可重复")
	flags.Var(&opts.levels, "level", "精确匹配等级，可重复："+strings.Join(levels, ", "))
	flags.Var(&opts.keywords, "keyword", "搜索 message/fields 的字面关键词，可重复")
	flags.Var(&patterns, "regex", "搜索 message/fields 的 Go 正则，大小写由表达式控制，可重复")
	flags.StringVar(&opts.since, "since", "-1h", "journal 起始时间，例如 -2h、today、now")
	flags.StringVar(&opts.until, "until", "", "journal 结束时间，用于固定时间段查询")
	flags.IntVar(&opts.lines, "lines", 100, "最多输出的匹配条数；0 为全部，跟踪默认不限")
	flags.BoolVar(&opts.follow, "follow", false, "从 since 起持续跟踪，达到显式条数上限后退出")
	flags.BoolVar(&opts.asJSON, "json", false, "日志输出完整 JSON 正文，目录输出 JSON 数组")
	flags.BoolVar(&opts.ignoreKeywordCase, "ignore-case", false, "关键词忽略大小写")
	flags.BoolVar(&opts.colour, "colour", false, "按等级和字段启用 ANSI 彩色输出，默认关闭")
	flags.BoolVar(&opts.list, "list", false, "展示本机日志名称目录，支持项目、实例和组件筛选")
	// 同一份有序名称表用于注册缩写和展示帮助，说明统一取自完整参数。
	names := []struct {
		long  string
		short string
	}{
		{long: "project", short: "p"},
		{long: "instance-id", short: "I"},
		{long: "component", short: "c"},
		{long: "level", short: "l"},
		{long: "since", short: "S"},
		{long: "until", short: "U"},
		{long: "keyword", short: "k"},
		{long: "regex", short: "e"},
		{long: "follow", short: "f"},
		{long: "ignore-case", short: "i"},
		{long: "lines", short: "n"},
		{long: "json"},
		{long: "list"},
		{long: "colour"},
	}
	for _, name := range names {
		if name.short != "" {
			option := flags.Lookup(name.long)
			flags.Var(option.Value, name.short, option.Usage)
		}
	}
	flags.Usage = func() {
		fmt.Fprintln(help, "用法：mct-syslogger [选项]\n展示本机日志名称目录，或检索 journal 中的 syslogger JSON 日志。")
		writer := tabwriter.NewWriter(help, 0, 4, 2, ' ', 0)
		for _, name := range names {
			option := flags.Lookup(name.long)
			label := "--" + name.long
			if name.short != "" {
				label = "-" + name.short + ", " + label
			}
			valueName, usage := flag.UnquoteUsage(option)
			if valueName != "" {
				label += " " + valueName
			}
			if option.DefValue != "" && option.DefValue != "false" && option.DefValue != "0" {
				usage += "（默认 " + option.DefValue + "）"
			}
			fmt.Fprintf(writer, "  %s\t%s\n", label, usage)
		}
		writer.Flush()
		fmt.Fprintln(help, "身份/等级同类取 OR，其他条件取 AND；普通查询默认最新 100 条，跟踪默认不限。")
	}
	if err := flags.Parse(args); err != nil {
		return opts, err
	}
	if flags.NArg() != 0 {
		return opts, fmt.Errorf("不支持位置参数：%s", strings.Join(flags.Args(), " "))
	}
	if opts.lines < 0 {
		return opts, errors.New("--lines 必须为非负整数")
	}
	if opts.follow && opts.until != "" {
		return opts, errors.New("--follow 用于跟踪新日志，--until 用于固定时间段查询，请分别使用")
	}
	if opts.list {
		var incompatible string
		flags.Visit(func(option *flag.Flag) {
			switch option.Name {
			case "list", "project", "p", "instance-id", "I", "component", "c", "json", "colour":
			default:
				incompatible = option.Name
			}
		})
		if incompatible != "" {
			return opts, fmt.Errorf("--list 仅支持身份筛选和输出格式，不能同时使用 --%s", incompatible)
		}
	}
	linesSet := false
	flags.Visit(func(value *flag.Flag) {
		if value.Name == "n" || value.Name == "lines" {
			linesSet = true
		}
	})
	if opts.follow && !linesSet {
		opts.lines = 0
	}
	for _, level := range opts.levels {
		if !contains(levels, level) {
			return opts, fmt.Errorf("未知日志等级 %q，可选：%s", level, strings.Join(levels, ", "))
		}
	}
	for _, pattern := range patterns {
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return opts, fmt.Errorf("正则表达式无效：%w", err)
		}
		opts.patterns = append(opts.patterns, compiled)
	}
	if opts.ignoreKeywordCase {
		for i, keyword := range opts.keywords {
			opts.keywords[i] = strings.ToLower(keyword)
		}
	}
	return opts, nil
}
