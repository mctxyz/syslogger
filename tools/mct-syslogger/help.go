package main

import (
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/mctxyz/syslogger/internal/catalog"
)

type optionName struct {
	long  string
	short string
}

// optionGroups 同时定义短参数注册和帮助分组，参数说明与默认值来自 FlagSet。
var optionGroups = []struct {
	title string
	names []optionName
}{
	{
		title: "模式选择",
		names: []optionName{{long: "list"}},
	},
	{
		title: "身份筛选（日志 / 目录通用）",
		names: []optionName{
			{long: "project", short: "p"},
			{long: "instance-id", short: "i"},
			{long: "component", short: "c"},
		},
	},
	{
		title: "日志等级（仅日志查询）",
		names: []optionName{
			{long: "level", short: "l"},
			{long: "exclude-level"},
		},
	},
	{
		title: "时间与数量（仅日志查询）",
		names: []optionName{
			{long: "since", short: "s"},
			{long: "until", short: "u"},
			{long: "lines", short: "n"},
			{long: "follow", short: "f"},
		},
	},
	{
		title: "内容搜索（仅日志查询）",
		names: []optionName{
			{long: "keyword", short: "k"},
			{long: "regex", short: "e"},
			{long: "ignore-case"},
		},
	},
	{
		title: "输出与帮助（日志 / 目录通用）",
		names: []optionName{
			{long: "json"},
			{long: "colour"},
		},
	},
}

// writeHelp 将参数参考和排障规则集中展示，查询结果的输出格式保持独立。
func writeHelp(output io.Writer, flags *flag.FlagSet) {
	fmt.Fprint(output, `mct-syslogger — 本机 syslogger 日志目录与 journal 检索

用法：
  mct-syslogger [筛选参数] [时间参数] [输出参数]
  mct-syslogger --list [身份筛选] [输出参数]
  mct-syslogger --help

查询方式：
  普通查询（默认）
    最近 1 小时内最新 100 条匹配日志，按 journal 从旧到新显示。
  实时跟踪（-f）
    先输出起始时间以来的匹配历史，再等待新日志，默认不限条数。
  名称目录（--list）
`)
	fmt.Fprintf(output, "    读取 %s，按 项目 → 实例 → 组件 展示名称及描述。\n", catalog.Root)
	fmt.Fprintln(output, "    由启用 CatalogEnabled 的 SDK 发布，进程退出后保留，可用于查历史身份。")

	writer := tabwriter.NewWriter(output, 30, 4, 2, ' ', 0)
	for _, group := range optionGroups {
		fmt.Fprintf(writer, "\n%s：\n", group.title)
		for _, name := range group.names {
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
				scope := "默认"
				if name.long == "lines" {
					scope = "普通查询默认"
				}
				usage += "（" + scope + " " + option.DefValue + "）"
			}
			fmt.Fprintf(writer, "  %s\t%s\n", label, usage)
		}
	}
	// Go flag 在未注册同名参数时自动处理 -h 和 --help。
	fmt.Fprintln(writer, "  -h, --help\t显示完整帮助并退出")
	writer.Flush()

	fmt.Fprintln(output, "\n可用等级（使用小写名称，按名称精确匹配）：")
	fmt.Fprintln(output, "  "+strings.Join(levels, ", "))
	fmt.Fprint(output, `
参数与组合规则：
  • 带大写占位符的参数需要一个值，如 -i local-a、--since=-2h。
    含空格、正则或特殊字符的值请加引号；布尔参数支持 =true / =false，如 --colour=false。
    所有输入均通过参数传入，位置参数会报错。
  • project / instance_id / component 和等级均精确匹配、区分大小写。
    未指定身份或包含等级时，该项不限；身份使用目录中的名称原文。
  • 同类身份和 --level 重复取 OR，不同筛选条件取 AND。
    例如 -c rpc -c worker -l error 表示 (rpc 或 worker) 且 error。
  • --exclude-level 排除任一指定等级；与 --level 同用时从包含集合中排除。
    例如 -l error --exclude-level error 将得到空结果。所有筛选均先于条数计数。
  • 每个 -k 和每个 -e 都必须匹配；单项可在 message 或 fields JSON 任一处命中。
    fields 搜索包含字段名及嵌套内容；身份、时间、目录描述不参与内容搜索。
    --ignore-case 只影响 -k；正则用 (?i) 忽略大小写、用 | 表达 OR。
    正则使用 Go regexp 语法（RE2），不支持前后向断言和反向引用。
  • --list 只接受 -p / -i / -c、--json、--colour（及其长参数形式）。
    组件筛选后显示匹配组件及所属实例；目录缺省或无匹配时为空结果。
    目录记录是历史身份线索，进程是否仍在运行应结合实际日志判断。

时间与条数：
  • -s / -u 交给本机 journalctl 解释，筛选的是 journal 收录时间。
    例如 -s=-2h、-s today、-s now，或 -s '2026-10-23 09:00:00 UTC'。
    无时区的绝对时间按设备本地时区解释；指定 UTC 可避免跨设备时区歧义。
  • 普通查询 -n N 选取范围内最新 N 条匹配记录，再按 journal 从旧到新输出。
    -n 0 输出范围内全部匹配记录；扩大历史范围须同时调整 -s。
  • -f 未指定 -n 时不限条数；显式 -n N 同时累计历史和新日志，达到上限退出。
    只看新日志用 -s now -f；固定时间段用 -s 和 -u，--until 与 --follow 互斥。
    自动化跟踪宜指定正数 -n；匹配不足时仍会等待，可由调用方设置执行时限。

输出与退出码：
  • 默认可读日志：UTC时间 等级 project/instance_id/component 消息 fields。
    时间来自正文 timestamp，显示到秒；换行和控制字符转义为单行。
    正文时间和 journal 收录时间可能因交付延迟而不同；JSON 保留原始时间精度。
  • --json：日志每行一条原始 JSON 正文（NDJSON），保留业务字段，不含 journal 元数据。
    --list --json：实例对象数组，字段为 project、instance_id、instance_description、components；
    components 中每项含 name、description。空目录输出 []，可读空目录无文本。
  • --colour 在可读输出中按等级和字段着色，重定向也保留 ANSI；JSON 始终无颜色。
    agent 和脚本建议使用 --json，或保持默认纯文本输出。
  • 查询结果写 stdout；帮助、参数错误和 journalctl 诊断写 stderr。
    退出码：0 成功 / 无匹配 / 帮助；1 查询失败；2 参数或正则错误；130 收到中断。
    journalctl 的权限或时间格式错误属于查询失败，返回 1，详细原因见 stderr。

排障示例（将 contract-listener / local-a / rpc 替换为实际身份）：
  查看本机身份目录，选择项目、实例和组件：
    mct-syslogger --list
    mct-syslogger --list -p contract-listener --json

  查询一个实例的 RPC 组件，最近两小时最新 200 条：
    mct-syslogger -p contract-listener -i local-a -c rpc -s=-2h -n 200

  查看 error 或 critical；或排除 debug：
    mct-syslogger -i local-a -l error -l critical
    mct-syslogger -i local-a --exclude-level debug

  消息或业务字段必须同时包含 timeout 和 retry，关键词忽略大小写：
    mct-syslogger -i local-a -k timeout -k retry --ignore-case

  匹配 timeout 或 connection refused，正则忽略大小写：
    mct-syslogger -i local-a -e '(?i)(timeout|connection refused)'

  固定 UTC 时间段的全部匹配日志，输出 NDJSON：
    mct-syslogger -i local-a -s '2026-10-23 09:00:00 UTC' -u '2026-10-23 10:00:00 UTC' -n 0 --json

  从现在起跟踪并排除 debug；或从最近 10 分钟跟踪，累计匹配 20 条后退出：
    mct-syslogger -i local-a -s now -f --exclude-level debug
    mct-syslogger -i local-a -s=-10m -f -n 20 --json

无结果与权限排查：
  1. 默认只查最近 1 小时；查启动记录或较早事件时，先扩大 -s，如 -s=-24h -n 0。
  2. 用 --list 核对身份原文，逐步减少身份、等级和内容条件；目录退出后仍保留。
  3. 日志查询需要 Linux、journalctl 和 journal 读取权限；可见范围受权限及留存策略影响。
     工具只展示符合 syslogger 结构的 JSON 正文，目录缺省时仍可手动指定身份查询。
  4. 需要提升读取权限时，可使用 sudo mct-syslogger ...。
     管理员也可将普通用户加入 systemd-journal 组：
       sudo usermod -aG systemd-journal username
     将 username 替换为实际用户名，退出并重新登录后生效。
     本工具只查询日志与目录，权限配置由管理员单独执行。
`)
}
