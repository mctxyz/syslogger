package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// levels 按严重程度从高到低排列，对应 SDK 的 severity 0～7，顺序用于解析查询阈值。
var levels = []string{
	"emergency",
	"alert",
	"critical",
	"error",
	"warning",
	"notice",
	"info",
	"debug",
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

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

// options 保存解析后的查询条件，正则在启动 journalctl 前统一编译。
// levels 是阈值、包含和排除条件合并后的有效集合，空集合表示无匹配。
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
	debug             bool
	version           bool
}

func parseOptions(args []string, help io.Writer) (options, error) {
	var opts options
	var level string
	var includedLevels, excludedLevels, patterns stringList
	flags := flag.NewFlagSet("mct-syslogger", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Var(&opts.projects, "project", "项目名 `PROJECT`；精确匹配，可重复")
	flags.Var(&opts.instances, "instance-id", "实例 ID `INSTANCE`；精确匹配，可重复")
	flags.Var(&opts.components, "component", "组件名 `COMPONENT`；精确匹配，可重复")
	flags.StringVar(&level, "level", "debug", "显示 `LEVEL` 及更严重等级；debug 表示全部")
	flags.Var(&includedLevels, "include-level", "只选指定等级 `LEVEL`；可重复")
	flags.Var(&excludedLevels, "exclude-level", "排除指定等级 `LEVEL`；可重复")
	flags.Var(&opts.keywords, "keyword", "message / fields 包含字面文本 `TEXT`；可重复")
	flags.Var(&patterns, "regex", "message / fields 匹配 Go 正则 `REGEX`；可重复")
	flags.StringVar(&opts.since, "since", "-1h", "起始时间 `TIME`；如 -2h、today、now 或绝对时间")
	flags.StringVar(&opts.until, "until", "", "结束时间 `TIME`；默认不限，与 -f 互斥")
	flags.IntVar(&opts.lines, "lines", 100, "最多 `N` 条匹配日志；0 为全部，跟踪默认不限")
	flags.BoolVar(&opts.follow, "follow", false, "持续跟踪，默认先读历史；-s now 只看新日志")
	flags.BoolVar(&opts.asJSON, "json", false, "机器可读输出：目录为数组，日志为 NDJSON")
	flags.BoolVar(&opts.ignoreKeywordCase, "ignore-case", false, "仅让 -k 关键词忽略大小写")
	flags.BoolVar(&opts.colour, "colour", false, "启用 ANSI 彩色输出；默认关闭，--colour=false 关闭")
	flags.BoolVar(&opts.list, "list", false, "列出本机项目 → 实例 → 组件；支持 -p / -i / -c")
	flags.BoolVar(&opts.debug, "debug", false, "向 stderr 输出查询耗时和扫描统计，与 -l debug 无关")
	flags.BoolVar(&opts.version, "version", false, "显示编译版本并退出")
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
	if opts.version {
		return opts, nil
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
	for _, selected := range []stringList{{level}, includedLevels, excludedLevels} {
		for _, name := range selected {
			if !contains(levels, name) {
				return opts, fmt.Errorf("未知日志等级 %q，可选：%s", name, strings.Join(levels, ", "))
			}
		}
	}
	// 等级表按严重程度从高到低排列，收集到阈值为止，再由正反选限定实际集合。
	for _, candidate := range levels {
		included := len(includedLevels) == 0 || contains(includedLevels, candidate)
		if included && !contains(excludedLevels, candidate) {
			opts.levels = append(opts.levels, candidate)
		}
		if candidate == level {
			break
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
