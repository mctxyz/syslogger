# 日志检索工具

`mct-syslogger` 提供本机日志名称目录和日志检索。目录查询读取共享 JSON，日志检索通过 Go 标准库调用本机 `journalctl`，解析 syslogger 正文并过滤。源码位于 [mct-syslogger](mct-syslogger)，入口、参数、目录显示和 journal 查询按职责分文件组织。编译后只需分发一个二进制；日志检索需要使用 systemd journal 的 Linux 及相应读取权限。

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

需要提升 journal 读取权限时，在命令前使用 `sudo`，例如 `sudo /path/to/syslogger/tools/bin/mct-syslogger -I local-a`。工具只执行查询，不更改日志权限和留存配置。`tools/bin/` 是已忽略的本地构建目录。

## 查看项目、实例与组件目录

业务开启 `Config.CatalogEnabled` 后，syslogger 向 `/tmp/mct-syslogger` 发布目录。`Config.InstanceDescription` 描述实例用途，组件通过 `Register(ComponentConfig)` 提供名称及 `Description`。目录开关默认关闭，与日志及指标出口独立。

```sh
# 查看全部项目、实例和组件。
slog --list

# 查看一个项目或一个实例。
slog --list -p contract-listener
slog --list -p contract-listener -I mainnet-a

# 按组件筛选，或输出机器可读的 JSON 数组。
slog --list -c rpc --colour
slog --list --json
```

树形输出示例：

```text
contract-listener
└── mainnet-a  主网事件监听
    ├── rpc  区块与日志获取
    └── worker  事件处理
```

目录按项目、实例和组件名称排序。`-p`、`-I`、`-c` 使用原文精确匹配，同类重复取 OR，不同条件取 AND；指定组件筛选后只显示匹配组件及其所属实例。`--json` 输出实例对象数组，其中包含 `project`、`instance_id`、`instance_description` 和 `components`。

目录查询直接读取固定路径，与各进程的 `TMPDIR` 无关，也可在未安装 `journalctl` 时使用。目录尚未创建或筛选无结果时正常返回空列表；JSON 格式错误和实际文件读取失败明确报错。目录模式只接受身份筛选、`--json` 和 `--colour`，日志时间、等级、内容、条数和跟踪参数用于日志查询模式。

清单在启动时重新生成、注册组件后更新，程序退出后保留，适合查找历史日志使用的名称。目录可由本机所有用户维护；项目和实例的磁盘文件名使用 SHA-256 摘要，JSON 保留身份原文。同一身份并行发布时最后完成的写入可见。临时目录被清理后，原有日志仍按 journal 留存范围查询，可以继续手动指定身份筛选。

## 查询示例

以下示例使用上方的 `slog` 别名：

```sh
# 某实例的 RPC 模块，最近一小时内最新 100 条匹配日志。
slog -p syslogger-test -I syslogger-test -c rpc

# 固定时间范围内的全部匹配日志。
slog -I local-a --since '2026-10-23 09:00:00' --until '2026-10-23 10:00:00' -n 0

# 最近两小时，消息或业务字段同时包含两个关键词。
slog -I local-a --since=-2h -k '请求' -k '超时'

# Go 正则：匹配任一错误描述，忽略大小写。
slog -c rpc -e '(?i)(timeout|connection refused)'

# 同时查看多个模块和多个等级。
slog -I local-a -c rpc -c worker -l error -l critical

# 按日志等级及项目、实例、模块、消息、业务字段分色显示。
slog -I local-a --colour

# 从现在起实时跟踪；Ctrl+C 退出。
slog -I local-a --since now -f

# 从最近 10 分钟开始跟踪，匹配 20 条后退出。
slog -I local-a --since=-10m -f -n 20

# 每行输出完整 JSON，可继续用其他工具分析。
slog -I local-a -n 500 --json
```

## 过滤和输出规则

| 参数 | 含义 |
| --- | --- |
| `-p / --project` | JSON `project` 原文精确匹配 |
| `-I / --instance-id` | JSON `instance_id` 原文精确匹配 |
| `-c / --component` | JSON `component` 原文精确匹配 |
| `-l / --level` | 等级精确匹配：`emergency`、`alert`、`critical`、`error`、`warning`、`notice`、`info`、`debug` |
| `-S / --since` | 起始时间，默认 `-1h`，支持 journalctl 的相对时间、`today` 和绝对时间 |
| `-U / --until` | 结束时间，配合起始时间查询固定范围 |
| `-k / --keyword` | 字面关键词，默认区分大小写 |
| `-e / --regex` | Go `regexp` 正则搜索，大小写由表达式控制 |
| `-i / --ignore-case` | 关键词忽略大小写 |
| `-n / --lines` | 匹配后的数量上限；`0` 表示全部 |
| `-f / --follow` | 从起始时间连续读取并即时输出，达到显式 `-n` 上限后退出 |
| `--json` | 输出原始正文内容的单行 JSON，保留业务字段 |
| `--list` | 展示项目／实例／组件目录，支持身份筛选及 JSON、彩色输出 |
| `--colour` | 开启 ANSI 彩色可读输出，默认关闭；`--colour=false` 显式关闭 |

- 同一个身份或等级参数重复时取 **OR**，不同参数之间取 **AND**。每个重复关键词、每个重复正则都必须匹配；正则中的 `|` 可表达 OR。
- 关键词和正则分别搜索 `message` 原文及 `fields` 的 JSON 表示（含字段名、嵌套内容）。每项可以在其中任一处匹配；身份与时间不参与搜索。
- 正则使用 [Go regexp 语法](https://pkg.go.dev/regexp/syntax)，支持分组、字符类及 `|`；大小写规则由表达式控制，例如 `(?i)timeout` 表示忽略大小写匹配，`(?i:timeout)` 只作用于该分组。
- 时间范围使用 **journal 收录时间**，由 journalctl 解释；未带时区的绝对时间按设备本地时区解释。可读输出使用正文中的 UTC `timestamp`，格式为 `YYYY-MM-DD HH:MM:SS`，截去秒以下精度；`--json` 保留原始时间。收录时间与正文时间可能因交付延迟而不同。
- 普通查询默认取最新 100 条匹配日志，再按 journal 从旧到新显示；`-n 0` 流式显示范围内全部匹配日志。数量限制在所有过滤之后执行。
- 跟踪默认输出起始时间以来的全部匹配历史并继续读取新日志，条数不限；只看新日志时用 `--since now -f`。`--until` 用于固定范围查询，与 `--follow` 分别使用。
- 身份按 JSON 原文筛选，不依赖规范化后的 syslog tag。日志量较大时缩小时间范围可减少扫描；工具只展示结构符合 syslogger 格式的 JSON 正文。
- 可读输出包含时间、等级、`project/instance_id/component`、消息及业务字段，换行和控制字符转义为单行。JSON 输出每行一条记录，不包含 journal 外层元数据。
- 彩色输出中，`project` 使用紫色、`instance_id` 使用蓝色、`component` 使用青色、`message` 使用绿色、`fields` 使用黄色；时间及分隔符保留终端默认色。等级配色为：`emergency` 红底加粗白字、`alert` 加粗亮紫、`critical` 亮红、`error` 红、`warning` 黄、`notice` 青、`info` 绿、`debug` 灰。
- `--colour` 输出 ANSI 转义序列，适用于支持 ANSI 颜色的终端，重定向时也保留颜色序列。纯文本输出使用默认设置或 `--colour=false`；`--json` 始终保留原始 JSON 正文。
- 无匹配结果正常返回 `0`；参数或正则错误返回 `2`，查询失败返回 `1`，Ctrl+C 返回 `130`。journalctl 的权限及时间格式诊断保留在标准错误输出中；可见日志范围取决于设备权限与留存策略。

## 代码级测试

在仓库目录执行：

```sh
go test -race ./tools/mct-syslogger ./internal/catalog
go vet ./tools/mct-syslogger ./internal/catalog
```

测试使用内存日志、隔离目录和测试辅助进程，覆盖目录发布与读取、筛选和输出，以及日志解析、数量限制、跟踪参数及收尾，不读取真实系统日志。
