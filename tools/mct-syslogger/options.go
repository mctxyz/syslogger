package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"regexp"
	"strings"
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
	excludedLevels    stringList
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
	flags.Var(&opts.projects, "project", "精确匹配项目名 `PROJECT`（JSON project）；可重复（OR）")
	flags.Var(&opts.instances, "instance-id", "精确匹配实例名 `INSTANCE`（JSON instance_id）；可重复（OR）")
	flags.Var(&opts.components, "component", "精确匹配组件名 `COMPONENT`（JSON component）；可重复（OR）")
	flags.Var(&opts.levels, "level", "仅保留指定等级 `LEVEL`；可重复（OR），按名称精确匹配")
	flags.Var(&opts.excludedLevels, "exclude-level", "排除指定等级 `LEVEL`；可重复，排除条件优先")
	flags.Var(&opts.keywords, "keyword", "搜索字面关键词 `TEXT`；默认区分大小写，可重复（AND）")
	flags.Var(&patterns, "regex", "搜索 Go 正则 `REGEX`；可重复（AND），用 (?i) 忽略大小写")
	flags.StringVar(&opts.since, "since", "-1h", "journal 起始时间 `TIME`；如 -2h、today、now 或带时区的绝对时间")
	flags.StringVar(&opts.until, "until", "", "journal 结束时间 `TIME`；默认无结束限制，与 --follow 互斥")
	flags.IntVar(&opts.lines, "lines", 100, "最多输出 `N` 条匹配日志；0 为全部，跟踪默认不限")
	flags.BoolVar(&opts.follow, "follow", false, "先输出 since 范围内历史，再持续等待新日志；Ctrl+C 退出")
	flags.BoolVar(&opts.asJSON, "json", false, "日志：每行一条原始 JSON（NDJSON）；目录：实例对象数组")
	flags.BoolVar(&opts.ignoreKeywordCase, "ignore-case", false, "仅关键词忽略大小写；身份、等级和正则保留各自匹配规则")
	flags.BoolVar(&opts.colour, "colour", false, "启用 ANSI 彩色可读输出，默认关闭；--colour=false 关闭")
	flags.BoolVar(&opts.list, "list", false, "查看本机项目、实例、组件名称及描述，支持身份筛选")
	for _, group := range optionGroups {
		for _, name := range group.names {
			if name.short != "" {
				option := flags.Lookup(name.long)
				flags.Var(option.Value, name.short, option.Usage)
			}
		}
	}
	flags.Usage = func() {
		writeHelp(help, flags)
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
			case "list", "project", "p", "instance-id", "i", "component", "c", "json", "colour":
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
	for _, selected := range []stringList{opts.levels, opts.excludedLevels} {
		for _, level := range selected {
			if !contains(levels, level) {
				return opts, fmt.Errorf("未知日志等级 %q，可选：%s", level, strings.Join(levels, ", "))
			}
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
