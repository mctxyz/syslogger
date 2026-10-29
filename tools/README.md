# 日志检索工具

`mct-syslogger` 提供本机日志名称目录和日志检索。目录查询读取共享 JSON，日志检索通过 Go 标准库调用本机 `journalctl`，解析 syslogger 正文并过滤。源码位于 [mct-syslogger](mct-syslogger)，入口、参数、帮助、目录显示和 journal 查询按职责分文件组织。编译后只需分发一个二进制；日志检索需要使用 systemd journal 的 Linux 及相应读取权限。

## 运行与快捷命令

在仓库目录使用 Go 1.19+ 编译并运行：

```sh
go build -o tools/bin/mct-syslogger ./tools/mct-syslogger
./tools/bin/mct-syslogger --help
./tools/bin/mct-syslogger --project syslogger-test --instance-id syslogger-test --component rpc
```

可以在 shell 配置中添加别名（替换为实际仓库绝对路径）：

```sh
alias slog='/path/to/syslogger/tools/bin/mct-syslogger'
```

也可以直接复制二进制到 PATH 中的个人目录，作为独立命令使用：

```sh
mkdir -p "$HOME/.local/bin"
install -m 755 /path/to/syslogger/tools/bin/mct-syslogger "$HOME/.local/bin/mct-syslogger"
```

需要提升 journal 读取权限时，在命令前使用 `sudo`，例如 `sudo /path/to/syslogger/tools/bin/mct-syslogger -i local-a`。工具只执行查询，不更改日志权限和留存配置。`tools/bin/` 是已忽略的本地构建目录。

要让普通用户直接读取系统 journal，管理员可将其加入 `systemd-journal` 组（将 `username` 替换为实际用户名）：

```sh
sudo usermod -aG systemd-journal username
```

退出并重新登录后组权限生效。命令的 `--help` 同时提供这一配置示例。

## 查看项目、实例与组件目录

业务开启 `Config.CatalogEnabled` 后，syslogger 向 `/tmp/mct-syslogger` 发布目录。`Config.InstanceDescription` 描述实例用途，组件通过 `Register(ComponentConfig)` 提供名称及 `Description`。目录开关默认关闭，与日志及指标出口独立。

```sh
# 查看全部项目、实例和组件。
slog --list

# 查看一个项目或一个实例。
slog --list -p contract-listener
slog --list -p contract-listener -i mainnet-a

# 按组件筛选，或输出机器可读的 JSON 数组。
slog --list -c rpc --colour
slog --list --json
```

树形输出示例：

```text
mct-syslogger
├── contract-listener
│   └── mainnet-a  主网事件监听
│       ├── rpc  区块与日志获取
│       └── worker  事件处理
└── other-project
    └── main  后台任务服务
        └── worker  任务处理
```

树形目录以 `mct-syslogger` 为统一展示根节点，其下依次为项目、实例和组件，并按名称排序。`-p`、`-i`、`-c` 使用原文精确匹配，同类重复取 OR，不同条件取 AND；指定组件筛选后只显示匹配组件及其所属实例。`--json` 输出实例对象数组，其中包含 `project`、`instance_id`、`instance_description` 和 `components`；每个组件包含 `name` 和 `description`。

目录查询直接读取固定路径，与各进程的 `TMPDIR` 无关，也可在未安装 `journalctl` 时使用。目录尚未创建或筛选无结果时正常返回空列表；JSON 格式错误和实际文件读取失败明确报错。目录模式只接受身份筛选、`--json` 和 `--colour`，日志时间、等级、内容、条数和跟踪参数用于日志查询模式。

清单在启动时重新生成、注册组件后更新，程序退出后保留，适合查找历史日志使用的名称。目录可由本机所有用户维护；项目和实例的磁盘文件名使用 SHA-256 摘要，JSON 保留身份原文。同一身份并行发布时最后完成的写入可见。临时目录被清理后，原有日志仍按 journal 留存范围查询，可以继续手动指定身份筛选。

## 查询示例

`mct-syslogger --help` 按模式、身份、等级、时间、内容和输出分组展示全部参数，同时说明组合规则、退出码、权限及排障流程。推荐先用 `--list` 核对身份，再查询日志；agent 可用 `--list --json` 获取实例对象数组，用日志查询的 `--json` 获取 NDJSON。

以下示例使用上方的 `slog` 别名：

```sh
# 某实例的 RPC 模块，最近一小时内最新 100 条匹配日志。
slog -p syslogger-test -i syslogger-test -c rpc

# 固定 UTC 时间范围内的全部匹配日志。
slog -i local-a -s '2026-10-23 09:00:00 UTC' -u '2026-10-23 10:00:00 UTC' -n 0

# 最近两小时，消息或业务字段同时包含两个关键词。
slog -i local-a -s=-2h -k '请求' -k '超时'

# 字面关键词忽略大小写；每个关键词都须在消息或业务字段中匹配。
slog -i local-a -k timeout -k retry --ignore-case

# Go 正则：匹配任一错误描述，忽略大小写。
slog -c rpc -e '(?i)(timeout|connection refused)'

# 同时查看多个模块和多个等级。
slog -i local-a -c rpc -c worker -l error -l critical

# 排除 debug，或同时排除多个等级。
slog -i local-a --exclude-level debug
slog -i local-a --exclude-level debug --exclude-level info

# 按日志等级及项目、实例、模块、消息、业务字段分色显示。
slog -i local-a --colour

# 从现在起实时跟踪；Ctrl+C 退出。
slog -i local-a -s now -f

# 从最近 10 分钟开始跟踪，匹配 20 条后退出。
slog -i local-a -s=-10m -f -n 20

# 每行输出完整 JSON，可继续用其他工具分析。
slog -i local-a -n 500 --json
```

## 过滤和输出规则

| 参数 | 含义 |
| --- | --- |
| `-p / --project PROJECT` | JSON `project` 原文精确匹配，可重复（OR） |
| `-i / --instance-id INSTANCE` | JSON `instance_id` 原文精确匹配，可重复（OR） |
| `-c / --component COMPONENT` | JSON `component` 原文精确匹配，可重复（OR） |
| `-l / --level LEVEL` | 等级精确匹配，可重复（OR）：`emergency`、`alert`、`critical`、`error`、`warning`、`notice`、`info`、`debug` |
| `--exclude-level LEVEL` | 排除指定等级，可重复；可选等级与 `--level` 相同 |
| `-s / --since TIME` | 起始时间，默认 `-1h`，支持 journalctl 的相对时间、`today`、`now` 和绝对时间 |
| `-u / --until TIME` | 结束时间，默认无结束限制；与 `--follow` 互斥 |
| `-k / --keyword TEXT` | 字面关键词，默认区分大小写；可重复（AND） |
| `-e / --regex REGEX` | Go `regexp` 正则搜索，大小写由表达式控制；可重复（AND） |
| `--ignore-case` | 仅关键词忽略大小写；正则用 `(?i)` 控制，身份和等级仍精确匹配 |
| `-n / --lines N` | 匹配后的数量上限；普通查询默认 `100`，跟踪默认不限，`0` 表示全部 |
| `-f / --follow` | 先输出起始时间以来的历史，再等待新日志；显式 `-n` 同时累计历史和新日志，达到上限后退出 |
| `--json` | 日志每行一条原始 JSON 正文（NDJSON），保留业务字段；目录输出实例对象数组 |
| `--list` | 展示项目／实例／组件目录，支持身份筛选及 JSON、彩色输出 |
| `--colour` | 开启 ANSI 彩色可读输出，默认关闭；`--colour=false` 显式关闭 |
| `-h / --help` | 显示完整帮助并退出 |

- 表中大写名称为值占位符，实际使用时替换为一个参数值。含空格或正则的值用引号包裹，如 `-s '2026-10-23 09:00:00 UTC'`、`-e '(?i)timeout'`；以 `-` 开头的时间值建议使用 `-s=-2h`。布尔参数直接开启或用 `--colour=false` 显式关闭，所有输入均通过参数传入。
- 同一个身份或等级参数重复时取 **OR**，不同参数之间取 **AND**。每个重复关键词、每个重复正则都必须匹配；正则中的 `|` 可表达 OR。
- 多个 `--exclude-level` 排除其中任一等级的记录；与 `--level` 同时使用时，从包含范围中再排除指定等级。数量上限在等级排除及其他过滤之后执行。
- 关键词和正则分别搜索 `message` 原文及 `fields` 的 JSON 表示（含字段名、嵌套内容）。每项可以在其中任一处匹配；身份与时间不参与搜索。
- 正则使用 [Go regexp 语法](https://pkg.go.dev/regexp/syntax)（RE2），支持分组、字符类及 `|`，不支持前后向断言和反向引用；大小写规则由表达式控制，例如 `(?i)timeout` 表示忽略大小写匹配，`(?i:timeout)` 只作用于该分组。
- 时间范围使用 **journal 收录时间**，由 journalctl 解释；未带时区的绝对时间按设备本地时区解释。可读输出使用正文中的 UTC `timestamp`，格式为 `YYYY-MM-DD HH:MM:SS`，截去秒以下精度；`--json` 保留原始时间。收录时间与正文时间可能因交付延迟而不同。
- 普通查询默认取最新 100 条匹配日志，再按 journal 从旧到新显示；`-n 0` 流式显示范围内全部匹配日志。数量限制在所有过滤之后执行。
- 跟踪默认输出起始时间以来的全部匹配历史并继续读取新日志，条数不限；只看新日志时用 `--since now -f`。`--until` 用于固定范围查询，与 `--follow` 分别使用。
- 自动化跟踪宜显式指定正数 `-n`；匹配不足时仍会持续等待，调用方可另设执行时限。查询较早的事件时，同时扩大 `-s`，因为 `-n 0` 只改变条数上限。
- 身份按 JSON 原文筛选，不依赖规范化后的 syslog tag。日志量较大时缩小时间范围可减少扫描；工具只展示结构符合 syslogger 格式的 JSON 正文。
- 可读输出包含时间、等级、`project/instance_id/component`、消息及业务字段，换行和控制字符转义为单行。JSON 输出每行一条记录，不包含 journal 外层元数据。
- 彩色输出中，`project` 使用紫色、`instance_id` 使用蓝色、`component` 使用青色、`message` 使用绿色、`fields` 使用黄色；时间及分隔符保留终端默认色。等级配色为：`emergency` 红底加粗白字、`alert` 加粗亮紫、`critical` 亮红、`error` 红、`warning` 黄、`notice` 青、`info` 绿、`debug` 灰。
- `--colour` 输出 ANSI 转义序列，适用于支持 ANSI 颜色的终端，重定向时也保留颜色序列。纯文本输出使用默认设置或 `--colour=false`；`--json` 始终保留原始 JSON 正文。
- 无匹配结果正常返回 `0`；参数或正则错误返回 `2`，查询失败返回 `1`，Ctrl+C 返回 `130`。journalctl 的权限或时间格式错误属于查询失败，返回 `1`，诊断保留在标准错误输出中；可见日志范围取决于设备权限与留存策略。
- 查询结果写入 stdout，帮助及诊断写入 stderr。无结果时，先扩大时间范围，再核对身份和组合条件，最后检查 journal 权限及留存范围；名称目录提供历史身份线索，进程运行情况需结合实际日志判断。

## 代码级测试

在仓库目录执行：

```sh
go test -race ./tools/mct-syslogger ./internal/catalog
go vet ./tools/mct-syslogger ./internal/catalog
```

测试使用内存日志、隔离目录和测试辅助进程，覆盖目录发布与读取、筛选和输出，以及日志解析、数量限制、跟踪参数及收尾，不读取真实系统日志。
