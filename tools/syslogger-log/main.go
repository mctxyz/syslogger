// Command syslogger-log 检索本机 journal 中的 syslogger 日志。
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
)

// levels 与库输出的八级日志正文名称一致。
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

// 字段使用固定 ANSI 前景色，每个片段结束后恢复终端样式。
const (
	colourReset     = "\x1b[0m"
	colourProject   = "\x1b[35m"
	colourInstance  = "\x1b[34m"
	colourComponent = "\x1b[36m"
	colourMessage   = "\x1b[32m"
	colourFields    = "\x1b[33m"
)

// levelColours 按严重程度分配独立样式，紧急事件通过背景及加粗突出显示。
var levelColours = map[string]string{
	"emergency": "\x1b[1;97;41m",
	"alert":     "\x1b[1;95m",
	"critical":  "\x1b[91m",
	"error":     "\x1b[31m",
	"warning":   "\x1b[33m",
	"notice":    "\x1b[36m",
	"info":      "\x1b[32m",
	"debug":     "\x1b[90m",
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
}

func parseOptions(args []string, help io.Writer) (options, error) {
	var opts options
	var patterns stringList
	flags := flag.NewFlagSet("syslogger-log", flag.ContinueOnError)
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
	flags.BoolVar(&opts.asJSON, "json", false, "每行输出一条完整 JSON 正文")
	flags.BoolVar(&opts.ignoreKeywordCase, "ignore-case", false, "关键词忽略大小写")
	flags.BoolVar(&opts.colour, "colour", false, "按等级和字段启用 ANSI 彩色输出，默认关闭")
	// 同一份有序名称表用于注册缩写和展示帮助，说明统一取自完整参数。
	names := []struct {
		long  string
		short string
	}{
		{
			long:  "project",
			short: "p",
		},
		{
			long:  "instance-id",
			short: "I",
		},
		{
			long:  "component",
			short: "c",
		},
		{
			long:  "level",
			short: "l",
		},
		{
			long:  "since",
			short: "S",
		},
		{
			long:  "until",
			short: "U",
		},
		{
			long:  "keyword",
			short: "k",
		},
		{
			long:  "regex",
			short: "e",
		},
		{
			long:  "follow",
			short: "f",
		},
		{
			long:  "ignore-case",
			short: "i",
		},
		{
			long:  "lines",
			short: "n",
		},
		{
			long:  "json",
			short: "",
		},
		{
			long:  "colour",
			short: "",
		},
	}
	for _, name := range names {
		if name.short != "" {
			option := flags.Lookup(name.long)
			flags.Var(option.Value, name.short, option.Usage)
		}
	}
	flags.Usage = func() {
		fmt.Fprintln(help, "用法：syslogger-log [选项]\n检索本机 journal 中的 syslogger JSON 日志。")
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

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// logRecord 保留业务字段的数字精度及原始 JSON，消息允许空字符串。
type logRecord struct {
	Timestamp  string         `json:"timestamp"`
	Level      string         `json:"level"`
	Project    string         `json:"project"`
	InstanceID string         `json:"instance_id"`
	Component  string         `json:"component"`
	Message    *string        `json:"message"`
	Fields     map[string]any `json:"fields"`
	raw        string
}

func parseRecord(line []byte) (logRecord, bool) {
	var record logRecord
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.UseNumber()
	if err := decoder.Decode(&record); err != nil {
		return record, false
	}
	// Decoder 读取单个值，检查尾部确保整行都是一条有效日志。
	if len(bytes.TrimSpace(line[decoder.InputOffset():])) != 0 {
		return record, false
	}
	if record.Timestamp == "" || record.Project == "" || record.InstanceID == "" ||
		record.Component == "" || record.Message == nil || !contains(levels, record.Level) {
		return record, false
	}
	record.raw = string(bytes.TrimSpace(line))
	return record, true
}

func (record logRecord) fieldsJSON() string {
	if len(record.Fields) == 0 {
		return ""
	}
	var text bytes.Buffer
	encoder := json.NewEncoder(&text)
	encoder.SetEscapeHTML(false)
	// 字段均来自 JSON 解码，值仅包含可编码的 JSON 类型。
	_ = encoder.Encode(record.Fields)
	return strings.TrimSuffix(text.String(), "\n")
}

func (opts options) matches(record logRecord) bool {
	for _, condition := range []struct {
		values []string
		value  string
	}{
		{
			values: opts.projects,
			value:  record.Project,
		},
		{
			values: opts.instances,
			value:  record.InstanceID,
		},
		{
			values: opts.components,
			value:  record.Component,
		},
		{
			values: opts.levels,
			value:  record.Level,
		},
	} {
		if len(condition.values) > 0 && !contains(condition.values, condition.value) {
			return false
		}
	}
	if len(opts.keywords) == 0 && len(opts.patterns) == 0 {
		return true
	}
	message := *record.Message
	fields := record.fieldsJSON()
	keywordMessage := message
	keywordFields := fields
	if opts.ignoreKeywordCase {
		keywordMessage = strings.ToLower(message)
		keywordFields = strings.ToLower(fields)
	}
	for _, keyword := range opts.keywords {
		if !strings.Contains(keywordMessage, keyword) && !strings.Contains(keywordFields, keyword) {
			return false
		}
	}
	for _, pattern := range opts.patterns {
		if !pattern.MatchString(message) && (fields == "" || !pattern.MatchString(fields)) {
			return false
		}
	}
	return true
}

func escaped(value string) string {
	quoted := strconv.Quote(value)
	return quoted[1 : len(quoted)-1]
}

func colourText(text string, style string) string {
	return style + text + colourReset
}

func (record logRecord) format(asJSON bool, colour bool) string {
	if asJSON {
		return record.raw
	}
	// 可读时间统一按 UTC 显示到秒，无法解析时保留转义后的原文供排查。
	timestamp := escaped(record.Timestamp)
	if at, err := time.Parse(time.RFC3339Nano, record.Timestamp); err == nil {
		timestamp = at.UTC().Format("2006-01-02 15:04:05")
	}
	// 等级先按可见字符补齐宽度，再添加 ANSI 样式，保持各行列对齐。
	level := fmt.Sprintf("%-9s", strings.ToUpper(escaped(record.Level)))
	project := escaped(record.Project)
	instance := escaped(record.InstanceID)
	component := escaped(record.Component)
	message := escaped(*record.Message)
	fields := record.fieldsJSON()
	if colour {
		level = colourText(level, levelColours[record.Level])
		project = colourText(project, colourProject)
		instance = colourText(instance, colourInstance)
		component = colourText(component, colourComponent)
		message = colourText(message, colourMessage)
		if fields != "" {
			fields = colourText(fields, colourFields)
		}
	}
	text := fmt.Sprintf(
		"%s %s %s/%s/%s %s",
		timestamp,
		level,
		project,
		instance,
		component,
		message,
	)
	if fields != "" {
		text += " " + fields
	}
	return text
}

func (opts options) journalArgs() []string {
	args := []string{
		"--no-pager",
		"--output=cat",
		"--all",
		"--since=" + opts.since,
	}
	if opts.until != "" {
		args = append(args, "--until="+opts.until)
	}
	if opts.follow {
		return append(args, "--follow", "--no-tail")
	}
	args = append(args, "--lines=all")
	if opts.lines > 0 {
		args = append(args, "--reverse")
	}
	return args
}

// filterLogs 的 complete 表示已读到 EOF；达到匹配上限时由调用方停止日志进程。
func filterLogs(input io.Reader, output io.Writer, opts options) (complete bool, err error) {
	reader := bufio.NewReader(input)
	var recent []logRecord
	count := 0
	for {
		// 单条日志按需分配内存，支持大于 Scanner 默认 64 KiB 的业务字段。
		line, readErr := reader.ReadBytes('\n')
		if record, valid := parseRecord(line); valid && opts.matches(record) {
			if !opts.follow && opts.lines > 0 {
				recent = append(recent, record)
			} else if _, err := fmt.Fprintln(output, record.format(opts.asJSON, opts.colour)); err != nil {
				return false, err
			}
			count++
			if opts.lines > 0 && count >= opts.lines {
				break
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				return false, readErr
			}
			complete = true
			break
		}
	}
	for i := len(recent) - 1; i >= 0; i-- {
		if _, err := fmt.Fprintln(output, recent[i].format(opts.asJSON, opts.colour)); err != nil {
			return false, err
		}
	}
	return complete, nil
}

func query(ctx context.Context, opts options, output, diagnostics io.Writer) error {
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	command := exec.CommandContext(childCtx, "journalctl", opts.journalArgs()...)
	command.Stderr = diagnostics
	stdout, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	if err := command.Start(); err != nil {
		_ = stdout.Close()
		return err
	}
	complete, readErr := filterLogs(stdout, output, opts)
	if !complete || readErr != nil {
		cancel()
	}
	waitErr := command.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if readErr != nil {
		return readErr
	}
	if complete && waitErr != nil {
		return fmt.Errorf("journalctl 查询失败：%w", waitErr)
	}
	return nil
}

func mainCode() int {
	opts, err := parseOptions(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "syslogger-log:", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// 让下游管道关闭转为写入错误，先回收 journalctl 再退出。
	signal.Ignore(syscall.SIGPIPE)
	if err := query(ctx, opts, os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, context.Canceled) {
			return 130
		}
		if errors.Is(err, syscall.EPIPE) {
			return 0
		}
		fmt.Fprintln(os.Stderr, "syslogger-log:", err)
		return 1
	}
	return 0
}

func main() {
	os.Exit(mainCode())
}
