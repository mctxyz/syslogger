# 日志检索工具

`mct-syslogger` 面向本 syslogger 模块，提供本机日志名称目录和日志检索。目录查询读取模块发布的共享 JSON；日志查询以模块的 syslog 输出为输入契约，通过 `journalctl` 的项目、等级索引预筛，再使用 [go-json v0.12.0](https://github.com/goccy/go-json/tree/v0.12.0) 解析正文并复核全部条件。源码位于 [mct-syslogger](mct-syslogger)，入口、参数、帮助、目录显示、记录解析、筛选、格式化和 journal 查询按职责分文件组织。运行时只需一个二进制；分发时同时保留 [第三方许可](THIRD_PARTY_NOTICES.md)。日志检索需要使用 systemd journal 的 Linux 及相应读取权限。

## 运行与快捷命令

在仓库目录使用 Go 1.23+ 编译并运行；当前验证工具链为 Go 1.23.12：

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

# 查看 info 及更严重的日志；debug 阈值表示全部等级。
slog -i local-a -l info

# 同时查看多个模块，精确选择 error 和 critical 两个等级。
slog -i local-a -c rpc -c worker --include-level error --include-level critical

# 排除 debug，或同时排除多个等级。
slog -i local-a --exclude-level debug
slog -i local-a --exclude-level debug --exclude-level info

# 组合阈值、精确包含和排除，最终仅显示 error。
slog -i local-a -l warning --include-level error --include-level critical --exclude-level critical

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
| `-p / --project PROJECT` | 先按模块项目 tag 预筛，再按 JSON `project` 原文精确匹配，可重复（OR） |
| `-i / --instance-id INSTANCE` | JSON `instance_id` 原文精确匹配，可重复（OR） |
| `-c / --component COMPONENT` | JSON `component` 原文精确匹配，可重复（OR） |
| `-l / --level LEVEL` | 最低严重程度阈值，显示该等级及更严重等级；默认 `debug`（全部等级），重复时最后一个生效 |
| `--include-level LEVEL` | 按等级名称精确包含，可重复（OR）；与 `--level` 的阈值范围取交集 |
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
| `--debug` | 仅日志查询：结束或中断后向 stderr 输出分阶段耗时、扫描及匹配统计，默认关闭 |
| `-h / --help` | 显示完整帮助并退出 |

- 表中大写名称为值占位符，实际使用时替换为一个参数值。含空格或正则的值用引号包裹，如 `-s '2026-10-23 09:00:00 UTC'`、`-e '(?i)timeout'`；以 `-` 开头的时间值建议使用 `-s=-2h`。布尔参数直接开启或用 `--colour=false` 显式关闭，所有输入均通过参数传入。
- 等级严重程度从高到低为：`emergency`、`alert`、`critical`、`error`、`warning`、`notice`、`info`、`debug`，参数使用小写名称。`--level info` 显示除 `debug` 外的七个等级；`--level warning` 显示 `warning`、`error`、`critical`、`alert`、`emergency`。`--level` 与 `-l` 共用一个阈值，重复时最后一个生效。
- 同一个身份参数或 `--include-level` 重复时取 **OR**，不同筛选条件之间取 **AND**。每个重复关键词、每个重复正则都必须匹配；正则中的 `|` 可表达 OR。
- 最终等级集合为阈值范围与包含集合的交集，再移除任一 `--exclude-level` 指定的等级。未指定包含条件时，包含范围不限；例如 `--level warning --include-level info --include-level error` 仅显示 `error`，`--include-level error --exclude-level error` 得到空结果。数量上限在全部等级筛选及其他过滤之后执行。
- 关键词和正则分别搜索 `message` 原文及 `fields` 的 JSON 表示（含字段名、嵌套内容）。每项可以在其中任一处匹配；身份与时间不参与搜索。
- 正则使用 [Go regexp 语法](https://pkg.go.dev/regexp/syntax)（RE2），支持分组、字符类及 `|`，不支持前后向断言和反向引用；大小写规则由表达式控制，例如 `(?i)timeout` 表示忽略大小写匹配，`(?i:timeout)` 只作用于该分组。
- 时间范围使用 **journal 收录时间**，由 journalctl 解释；未带时区的绝对时间按设备本地时区解释。可读输出将正文中的 `timestamp` 转换为运行设备的系统时区，格式为 `YYYY-MM-DD HH:MM:SS`，截去秒以下精度；`--json` 保留原始时间。收录时间与正文时间可能因交付延迟而不同。
- 普通查询默认取最新 100 条匹配日志，再按 journal 从旧到新显示；`-n 0` 流式显示范围内全部匹配日志。数量限制在所有过滤之后执行。
- 跟踪默认输出起始时间以来的全部匹配历史并继续读取新日志，条数不限；只看新日志时用 `--since now -f`。`--until` 用于固定范围查询，与 `--follow` 分别使用。
- 自动化跟踪宜显式指定正数 `-n`；匹配不足时仍会持续等待，调用方可另设执行时限。查询较早的事件时，同时扩大 `-s`，因为 `-n 0` 只改变条数上限。
- 模块 syslog 发送器将项目 tag 的空白及控制字符转为下划线，journal 将其记录为 `SYSLOG_IDENTIFIER`；协议头的 severity 0～7 对应 `PRIORITY`。工具默认把指定项目和最终等级集合交给 journalctl 原生预筛，同字段重复匹配取 OR，项目与等级之间取 AND。全等级查询使用完整等级范围，空等级集合匹配模块不可能产生的 priority，并保留 journalctl 的参数校验和错误反馈。
- 项目 tag 的规范化由发送器与查询工具共享；Go 使用正文项目原文继续精确复核，保证 tag 归一化碰撞时不会混入其他项目。实例、组件和内容条件在 Go 中筛选，journalctl 保持不限原生条数，达到全部筛选后的匹配上限才停止读取。
- 查询先一次提取基础字段并组合判断项目、实例、组件和等级，再逐项检查 message；内容条件需要访问 fields 或可读输出需要显示它时，才展开业务字段。fields 的规范化 JSON 至多编码一次，供全部内容条件和显示复用；`--json` 保留原文，message 已满足全部内容条件时可跳过 fields 展开。整行语法和字段类型在输出与计数前校验，数字精度、字段键排序和转义规则保持一致。
- journal 项目、等级预筛减少传入 Go 的无关正文，多级按需处理继续减少解析、对象分配和编码开销；缩小时间范围可进一步减少候选记录，实际总耗时也受 journal 索引、扫描及 I/O 影响。Go 1.23 已退出官方常规支持窗口，部署方需关注工具链安全维护。
- 可读输出包含时间、等级、`project/instance_id/component`、消息及业务字段，换行和控制字符转义为单行。JSON 输出每行一条记录，不包含 journal 外层元数据。
- 彩色输出中，时间及分隔符使用终端默认前景色和亮度，`project` 使用紫色、`instance_id` 使用蓝色、`component` 使用青色、`message` 使用绿色；`fields` 沿用终端默认前景色并弱化亮度（ANSI 2），通过明暗与时间区分，实际效果由终端主题和样式支持决定。等级配色为：`emergency` 红底加粗白字、`alert` 加粗亮紫、`critical` 亮红、`error` 红、`warning` 黄、`notice` 青、`info` 绿、`debug` 灰。
- `--colour` 输出 ANSI 转义序列，适用于支持 ANSI 颜色的终端，重定向时也保留颜色序列。纯文本输出使用默认设置或 `--colour=false`；`--json` 始终保留原始 JSON 正文。
- 无匹配结果正常返回 `0`；参数或正则错误返回 `2`，查询失败返回 `1`，Ctrl+C 返回 `130`。journalctl 的权限或时间格式错误属于查询失败，返回 `1`，诊断保留在标准错误输出中；可见日志范围取决于设备权限与留存策略。
- 查询结果写入 stdout，帮助及诊断写入 stderr。无结果时，先扩大时间范围，再核对身份和组合条件，最后检查 journal 权限及留存范围；名称目录提供历史身份线索，进程运行情况需结合实际日志判断。

## 查询性能诊断

在原有查询命令后添加 `--debug`，日志仍写入 stdout，性能汇总写入 stderr：

```sh
mct-syslogger -p contract-listener -i listener-v2 -l notice --debug

# 单独保存性能汇总和 journalctl 诊断。
mct-syslogger -p contract-listener -i listener-v2 -l notice --debug 2>query-debug.txt
```

该开关与 `-l debug`（显示全部等级的阈值）独立，可用 `--debug=false` 关闭。仅统计当前日志查询，查询完成、失败或 Ctrl+C 收尾后汇总一次；`--list` 使用目录查询流程，参数范围保持为身份筛选与目录输出选项。

| 输出字段 | 含义 |
| --- | --- |
| `query.total`、`stop` | 查询总墙钟耗时（含启动与收尾，汇总打印前截止）；停止原因是 `eof`、`limit`、`canceled` 或 `error` |
| `journalctl.args` | 实际交给 journalctl 的参数，用于核对项目、等级预筛条件、时间范围、反向读取和跟踪方式 |
| `time.start`、`read` | 进程启动耗时；行读取累计耗时，包含等待 journalctl、管道读取和缓冲处理 |
| `time.parse`、`filter` | 基础字段提取与整行合法性校验；身份、等级及内容筛选，后者包含按需 fields 处理 |
| `time.format`、`write` | 最终文本构造（含可读输出所需 fields）；stdout 写入累计耗时 |
| `time.wait`、`other` | 子进程取消及 Wait 收尾；其余查询编排、缓冲缓存和计时开销 |
| `first.line`、`match`、`output` | 从查询开始到首个完整输入行返回、首次匹配、首条完整输出写入的延迟；事件未发生时显示 `n/a` |
| `count.scanned_lines`、`scanned_bytes` | 原生预筛后已交付给 Go 过滤循环的输入行数和原始字节数，含换行与被正文复核排除的候选记录；不含未消费的预读余量，也不代表 journal 内部物理扫描量 |
| `count.valid`、`base_matches`、`matched`、`written` | 有效 syslogger 记录、通过身份与等级的记录、通过全部条件的记录、完整写出的记录数 |
| `fields.decode`、`encode`、`decodes`、`encodes` | 按需字段解码及规范化编码的累计耗时与实际调用次数；解码包含重新扫描根对象，空字段可解码但不编码 |

顶层 `time` 各阶段加上 `other` 对应 `total`；`fields` 是 `filter` 或 `format` 内的子阶段，**不能再次相加**。全部为本进程观察到的墙钟耗时，journalctl 与 Go 处理可能并行，不能由此直接分解 journalctl 的 CPU、磁盘读取或解压耗时。跟踪模式的空闲等待计入 `read`。

定位时优先关注：

- `read` 很大：时间主要花在等待或接收 journalctl 输出。结合 `scanned_lines` 判断是否扫描了大量日志；缩小 `-s / -u` 时间范围做对照，不能仅凭这个值认定磁盘故障。
- `scanned_lines` 很多、`base_matches` 很少：先从 `journalctl.args` 核对原生项目与等级条件，再判断实例、组件等正文条件是否命中稀疏。`base_matches` 较多但 `matched` 很少时，主要由内容条件继续排除候选记录。
- `parse / filter / format` 很大：继续看 fields 子阶段、实际解码次数和内容条件，区分基础解析、字段搜索和显示成本。
- `write` 很大：检查终端或下游管道是否消费缓慢。
- `first.match` 很早、`first.output` 很晚：普通查询会先收集最新 N 条匹配日志，再倒序输出；不足 N 条时会读到时间范围结束。默认 N 为 100，`-n 0` 则按读取顺序流式输出全部结果，二者选择语义不同。

`--debug` 会为每个阶段计时并累计计数，本身存在额外开销；关闭时不执行这些逐行计时。诊断输出失败按查询错误返回，已有查询错误仍可识别。

## 代码级测试

在仓库目录执行：

```sh
go test -race ./tools/mct-syslogger ./internal/catalog
go vet ./tools/mct-syslogger ./internal/catalog
GOTOOLCHAIN=go1.23.12 go test -run '^$' -bench '^(BenchmarkQueryRecord|BenchmarkFilterLogs)$' -benchmem ./tools/mct-syslogger
```

测试使用内存日志、隔离目录和测试辅助进程，覆盖目录发布与读取、筛选和输出，以及日志解析、数量限制、跟踪参数及收尾，不读取真实系统日志。JSON 差分测试和 fuzz 使用优化前标准库实现作为对照，覆盖重复键合并、空值、非法内容、精度、转义、按需展开及缓存所有权；基准分别测量单条 CPU 路径和含行读取、缓存反转、写入的完整过滤路径。原生预筛测试使用 SDK 编码的正文与元数据，覆盖等级阈值、包含/排除、多项目、tag 归一化碰撞、实例/组件/内容复核及匹配后限量。性能诊断测试覆盖分级计数、字段复用、读取/写入归属、stderr 分流、取消和错误；`BenchmarkFilterDebug` 对照开启与关闭诊断时的处理开销。
