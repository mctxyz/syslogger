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
		title: "查看目录与空间范围",
		names: []optionName{{long: "list"}, {long: "ranges"}},
	},
	{
		title: "身份筛选（目录和日志通用）",
		names: []optionName{
			{long: "project", short: "p"},
			{long: "instance-id", short: "i"},
			{long: "component", short: "c"},
		},
	},
	{
		title: "日志等级（仅日志）",
		names: []optionName{
			{long: "level", short: "l"},
			{long: "include-level"},
			{long: "exclude-level"},
		},
	},
	{
		title: "时间与数量（仅日志）",
		names: []optionName{
			{long: "since", short: "s"},
			{long: "until", short: "u"},
			{long: "lines", short: "n"},
			{long: "follow", short: "f"},
		},
	},
	{
		title: "内容搜索（仅日志）",
		names: []optionName{
			{long: "keyword", short: "k"},
			{long: "regex", short: "e"},
			{long: "ignore-case"},
		},
	},
	{
		title: "查询诊断（仅日志）",
		names: []optionName{{long: "debug"}},
	},
	{
		title: "输出与帮助",
		names: []optionName{
			{long: "json"},
			{long: "colour"},
			{long: "version"},
		},
	},
}

// writeHelp 先展示身份发现和常用查询，再提供完整参数与自动化契约。
func writeHelp(output io.Writer, flags *flag.FlagSet) {
	fmt.Fprint(output, `mct-syslogger — 查看本机日志、空间留存范围及项目 / 实例 / 组件目录

用法：
  mct-syslogger --version
  mct-syslogger --list [身份筛选] [--json] [--colour]
  mct-syslogger --ranges [--json] [--colour]
  mct-syslogger [身份筛选] [日志过滤参数] [输出参数]
  身份筛选：-p PROJECT、-i INSTANCE、-c COMPONENT，均可省略或重复。

1. 先找有哪些 project / instance / component
  三层目录依次是：project（项目）→ instance（运行实例）→ component（实例内组件）。
  树根 mct-syslogger 只是标题，下面三层才是可筛选的名称。
  example-project / example-instance / example-component 是示例，请替换为实际列出的名称。

  查看所有项目及其下的实例、组件：
    mct-syslogger --list
  查看某项目下的实例、组件：
    mct-syslogger --list -p example-project
  查看某项目某实例下的组件：
    mct-syslogger --list -p example-project -i example-instance
  组件行先显示最后接收时间，再显示当前小时及前两个小时的各小时接收量。
  数量表示 SDK 接收活动，不是 journal 当前留存条数。

  查看四个日志空间当前可查询的首尾时间：
    mct-syslogger --ranges
  按空间等级着色显示：
    mct-syslogger --ranges --colour
  输出 JSON 数组供脚本读取：
    mct-syslogger --ranges --json
  FIRST_ENTRY / LAST_ENTRY 是当前权限可读记录的首尾时间；- 表示暂无可读记录。
  每次执行重新查询，时间边界不保证中间连续，也不保证每个项目都覆盖该区间。

2. 用找到的名称查日志
  查看该组件近 1 小时内最新 100 条匹配日志：
    mct-syslogger -p example-project -i example-instance -c example-component
  查某项目近 2 小时、warning 及更严重、内容包含 timeout 的日志，最多 200 条：
    mct-syslogger -p example-project -s=-2h -l warning -k timeout -n 200
  只看 error 这一个等级（-l error 则还包含 critical / alert / emergency）：
    mct-syslogger -p example-project --include-level error
  从现在起持续跟踪该实例的新日志，Ctrl+C 退出：
    mct-syslogger -p example-project -i example-instance -s now -f

默认行为：省略身份表示不限该项；日志默认近 1 小时、全部等级、最多 100 条。
普通查询先选最新 N 条匹配记录，再从旧到新显示；-n 0 取消条数限制，仍受时间范围限制。

参数速查：
`)

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

	fmt.Fprintln(output, "\n3. 过滤怎么组合")
	fmt.Fprintln(output, "  等级从严重到轻微（使用小写）：")
	fmt.Fprintln(output, "  "+strings.Join(levels, ", "))
	fmt.Fprint(output, `
  身份：精确匹配、区分大小写；同类任选其一（OR），不同类同时满足（AND）。
    -c example-component -c example-component-2 表示这两个组件任选其一。
    实例名、组件名可能在不同项目下重复，建议同时指定 -p。
  等级：-l 是严重程度阈值；--include-level 可重复选择多个等级；--exclude-level 优先排除。
    同时设置阈值和精确选择时，只保留两者都允许的等级；重复 -l 取最后一个。
  内容：-k / -e 搜索 message 或 fields（含字段名）；每个关键词、每个正则都要匹配。
    -k timeout -k retry 表示两个词都要出现；-e '(?i)(timeout|refused)' 表示任一词，忽略大小写。
    默认区分大小写；--ignore-case 只作用于 -k，正则忽略大小写使用 (?i)。
  时间：筛选 journal 收录时间；绝对时间未写时区时按本机时区解释。
    如 -s=-2h，或 -s '2026-11-05 09:00:00 UTC' -u '2026-11-05 10:00:00 UTC'。
  跟踪：-f 按 -s 读取历史后等待新日志；默认近 1 小时，-s now -f 只看新日志。
    跟踪默认不限条数；-n N 同时计入历史和新日志，达到后退出；-f 与 -u 不能同用。
  目录：--list 支持身份筛选和输出格式；时间、等级和内容等日志筛选用于日志查询。
  空间范围：--ranges 支持 --json、--colour；按整个空间的 journal 收录时间查询。
  模式：--list 与 --ranges 不能同时开启；显式设置为 false 的模式或跟踪选项不产生互斥。
  修饰项：--colour 不参与互斥，JSON、版本和 init 输出不着色；--debug 仅日志查询生效。
    --ignore-case 仅作用于 -k；缺少对应功能时忽略修饰项，不影响目录身份匹配。
  写法：大写名称是待替换的参数值；含空格或正则的值加引号；查询条件都通过参数传入。

4. Agent / 脚本怎么用
  先读取机器可读目录，取得准确的名称：
    mct-syslogger --list --json
  目录是 JSON 数组：每项的 project、instance_id 对应 -p、-i，components[].name 对应 -c。
  组件的 log_counts 固定为三个小时项，各项用 start_time（UTC）和 count 表示起点与数量。
  相同 start_time 表示同一时段；新的起点是新的计数段，数量按对应小时合并。
  再指定身份、时间和数量查询；输出为 NDJSON，每行一个 JSON 对象：
    mct-syslogger -p example-project -i example-instance -c example-component -s=-2h -n 200 --json
  日志字段：timestamp、level、project、instance_id、component、message，以及可选 fields。
  结果写 stdout；帮助、错误和 --debug 查询统计写 stderr；默认无 ANSI 颜色。
  JSON 查询无匹配时：目录输出 []，日志无记录输出，均返回 0。
  空间范围 JSON 为四项数组，字段是 namespace、first_entry、last_entry。
  首尾时间保持 UTC 和微秒精度；空空间的两个时间为 null，查询失败返回错误。
  退出码：0=成功/无匹配/帮助；1=查询失败；2=参数/正则错误；130=收到中断。
  批处理优先使用不带 -f 的查询；跟踪即使指定 -n，匹配不足也会等待，需另设执行时限。
`)
	fmt.Fprintf(output, `
5. 目录为空或查不到日志？
  目录读取 %s；业务需开启 CatalogEnabled，并实际产生日志。
  正常更新可能滞后约 10 分钟，只列出一年内有日志接收活动的组件。
  目录不代表进程存活或 journal 仍有日志；已知名称可直接查询。
  日志默认只查近 1 小时；先扩大范围（-s=-24h -n 0），再减少过滤条件。
  日志固定读取 debug、info/notice、warning、error 及以上四个专用空间。
  需要 Linux、systemd 245+、journalctl 和读取权限；权限不足可用 sudo mct-syslogger ...。
  普通用户需要免 sudo 读取 journal 时，管理员也可按需配置日志组：
    sudo usermod -aG systemd-journal username
  将 username 替换为实际用户名；退出并重新登录后组权限生效。
  此组通常授予系统 journal 及其他命名空间的读取权限，不只本工具的四个空间。
  SDK 通过默认 socket 写日志通常无需加入此组；请按实际读取需求授权。
  开启名称目录的程序账户需加入 mct-syslogger 写入组，配置示例见 init --help。
  名称目录写入组与 systemd-journal 读取组分别授权；加组后重启程序以刷新组身份。
  查询模式只读；主机初始化由管理员操作，参数与影响先查看：
    mct-syslogger init --help
  安装配置与完整规则见项目 tools/README.md、docs/journal.md。
`, catalog.Root)
}
