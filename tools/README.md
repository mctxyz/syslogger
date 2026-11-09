# 日志检索工具

`mct-syslogger` 面向本 syslogger 模块，提供本机日志名称目录和日志检索。目录查询读取模块发布的共享 JSON；日志查询读取 debug、info/notice、warning、error 及以上四个专用 journal 空间，以模块的原生 journal JSON 正文为输入契约，通过 `journalctl` 的命名空间、身份和等级索引预筛，再使用 [go-json v0.12.0](https://github.com/goccy/go-json/tree/v0.12.0) 解析正文并复核全部条件。源码位于 [mct-syslogger](mct-syslogger)，入口、参数、帮助、目录显示、记录解析、筛选、格式化和 journal 查询按职责分文件组织。运行时只需一个二进制；分发时同时保留 [第三方许可](THIRD_PARTY_NOTICES.md)。日志检索需要 Linux、systemd 245+、journalctl 及相应读取权限，入口准备见[部署说明](../docs/journal.md)。

## 快速上手：先找名称，再查日志

目录的三层依次是 `project`（项目）、`instance`（运行实例）、`component`（实例内组件），树根 `mct-syslogger` 只是标题。下方 `example-project`、`example-instance`、`example-component` 都是示例，请替换成实际列出的名称：

```sh
# 看有哪些项目，以及各项目下的实例和组件。
mct-syslogger --list

# 逐层缩小目录范围，找到需要查询的组件。
mct-syslogger --list -p example-project
mct-syslogger --list -p example-project -i example-instance

# 使用找到的身份查询：默认近 1 小时、全部等级、最新 100 条匹配日志。
mct-syslogger -p example-project -i example-instance -c example-component

# 加时间、等级和内容过滤：最近 2 小时，warning 及更严重，包含 timeout。
mct-syslogger -p example-project -s=-2h -l warning -k timeout -n 200

# 只跟踪新日志；Ctrl+C 退出。
mct-syslogger -p example-project -i example-instance -s now -f

# Agent / 脚本先读 JSON 目录，再输出日志 NDJSON。
mct-syslogger --list --json
mct-syslogger -p example-project -i example-instance -c example-component -s=-2h -n 200 --json
```

目录 JSON 是实例对象数组：`project` 对应 `-p`，`instance_id` 对应 `-i`，`components[].name` 对应 `-c`。日志 JSON 为 NDJSON，每行一个日志对象。结果写 stdout，帮助、错误及 `--debug` 查询统计写 stderr。批处理优先不带 `-f`；跟踪即使指定 `-n`，匹配不足仍会等待，需要调用方设置执行时限。

同类身份重复时任选其一（OR），不同类身份同时满足（AND）；关键词和正则条件则全部要满足。`-l error` 表示 error 及更严重，`--include-level error` 表示只看 error。`-n 0` 只取消条数限制，查更早的日志仍要扩大 `-s`。目录是 SDK 发布的历史名称，不代表进程存活或 journal 留存；目录为空时，已知身份仍可直接查询。完整速查使用 `mct-syslogger --help`，管理员初始化的参数与影响使用 `mct-syslogger init --help`。

## Release 下载与安装

### 一条命令安装或更新

在目标 Linux 主机的交互终端执行：

```bash
bash -o pipefail -c 'curl -fsSL https://raw.githubusercontent.com/mctxyz/syslogger/main/tools/install/install.sh | bash'
```

[安装脚本](https://github.com/mctxyz/syslogger/blob/main/tools/install/install.sh) 自动识别 amd64／arm64，解析最新 Release 的固定 tag，下载对应压缩包并校验 `SHA256SUMS`。默认只询问安装前缀，确认后安装或更新命令及配套文档，保留日志配置、名称目录和 journald 服务状态；首次安装也是如此。

两种模式在安装写入前都只读检查四项容量及固定账户的参数接口；探针仅解析参数和显示帮助，不执行主机初始化。接口不符时停止，保留原有安装。行为更新以配套 Release 为准，参数检查不替代版本配套。

需要同时配置日志接收入口时，显式传入 `--init`：

```bash
bash -o pipefail -c 'curl -fsSL https://raw.githubusercontent.com/mctxyz/syslogger/main/tools/install/install.sh | bash -s -- --init'
```

也可下载脚本后运行 `bash install.sh --init`，或在程序安装后单独运行 `sudo mct-syslogger init`。两种脚本模式的交互参数如下：

| 参数 | 模式 | 默认值与用途 |
| --- | --- | --- |
| 安装前缀 | 两种模式 | `/usr/local`；命令安装到 `bin/mct-syslogger`，使用说明、许可和部署文档安装到 `share/doc/mct-syslogger/` |
| debug 持久化容量 | `--init` | `1G`，传给 `--journal-debug-max-use` |
| info/notice 持久化容量 | `--init` | `2G`，传给 `--journal-info-max-use` |
| warning 持久化容量 | `--init` | `1G`，传给 `--journal-warning-max-use` |
| error 及以上持久化容量 | `--init` | `1G`，传给 `--journal-error-max-use` |

默认模式显示版本和安装路径，确认后仅写入命令及文档。`--init` 模式另行显示固定所有者、四项容量及初始化影响，确认后安装并执行带四容量参数的 `mct-syslogger init`；容量接受正整数字节或 `K/M/G/T/P/E` 单位。普通用户仅在安装写入及显式初始化时通过 `sudo` 提权，root 可直接运行；无需对整个下载管道使用 `sudo`。可先下载并审阅脚本，再用 `bash install.sh --help` 查看说明。

安装需要 Linux、Bash、curl、GNU tar/coreutils、awk、root 或 sudo；`--init` 模式另需 getent、已有本机 `app` 账户、systemd 245+ 及初始化系统工具。日志检索仍需要 systemd 245+、journalctl 和相应读取权限。交互从 `/dev/tty` 读取，支持管道安装；没有控制终端时明确失败。自定义安装前缀的 `bin/` 若未在 PATH 中，可使用安装后显示的绝对路径运行命令。

`--init` 或直接执行 `mct-syslogger init` 会创建或核验名称根目录、重新生成带工具标识的受管 journal 配置并重启专用实例；受管文件中的手动修改会被覆盖。管理员独立配置保留并参与冲突检查，缩小容量可能清理旧归档。历史内容由部署方手动处理，清理前先停止相关写入进程。

下载或校验失败、取消确认均不会执行系统安装或初始化；安装或初始化失败返回非零，已经完成的步骤保留。命令安装后若显式初始化失败，请按错误指引人工处理后重试。日志读取权限继续由管理员单独管理。

### 手工下载安装

从 [GitHub Releases](https://github.com/mctxyz/syslogger/releases) 下载同一版本的 `SHA256SUMS` 及对应架构压缩包：

| 资产 | 运行平台 |
| --- | --- |
| `mct-syslogger_linux_amd64.tar.gz` | Linux x86-64 |
| `mct-syslogger_linux_arm64.tar.gz` | Linux ARM64 |
| `SHA256SUMS` | 两个压缩包的 SHA-256 校验值 |

以下以 amd64 为例，在下载目录校验并安装；`--ignore-missing` 允许只下载当前架构的压缩包。命令在独立子 shell 中执行，校验或安装失败时停止后续步骤：

```sh
(
    set -eu
    sha256sum --ignore-missing -c SHA256SUMS
    tar -xzf mct-syslogger_linux_amd64.tar.gz
    mkdir -p "$HOME/.local/bin"
    install -m 755 mct-syslogger_linux_amd64/mct-syslogger "$HOME/.local/bin/mct-syslogger"
    "$HOME/.local/bin/mct-syslogger" --help
)
```

压缩包以 `mct-syslogger_linux_<架构>/` 为根目录，包含命令二进制、`tools/README.md`、`tools/THIRD_PARTY_NOTICES.md` 和 `docs/journal.md`。分发时保留许可文件，日志查询前按包内 journal 部署说明准备专用命名空间。

### CI 与自动发布

[CI 工作流](https://github.com/mctxyz/syslogger/blob/main/.github/workflows/ci.yml) 在面向 `main` 的 PR、`main` 推送及任意 tag 推送时运行格式、依赖一致性、`go vet` 和全量 race 测试。工具链固定为 Go 1.23.12，并通过 `GOTOOLCHAIN=local` 使用该版本。

tag 的检查通过后，工作流使用 `CGO_ENABLED=0` 编译 Linux amd64、arm64 工具，通过 Go 链接参数 `-X main.version=$GITHUB_REF_NAME` 将触发发布的 tag 写入二进制，并校验 amd64 程序的 `--version` 输出；随后打包并生成校验文件，再创建对应 tag 的 GitHub Release、自动生成发布说明并上传上述三个资产。已有 Release 会补充或更新同名资产；启用不可变发布后，GitHub 会拒绝覆盖已锁定的资产，应使用新 tag 发布新版本。工作流需要包含在推送的 tag 所指提交中。

发布采用仓库内置的 `GITHUB_TOKEN`，只有发布任务获得 `contents: write`，无需配置额外 PAT。仓库需允许 GitHub Actions 运行及使用工作流中的 Actions；PR 只执行只读检查。工作流复用官方 checkout、setup-go、artifact Actions 和 [action-gh-release v3.0.3](https://github.com/softprops/action-gh-release/tree/v3.0.3)，均固定到 commit SHA。维护者在完成代码推送后，推送版本 tag 即可触发发布。

## 运行与快捷命令

在仓库目录使用 Go 1.23+ 编译并运行；当前验证工具链为 Go 1.23.12。本地构建用 Git 自动取得版本描述并写入二进制：

```sh
version=$(git describe --tags --always --dirty)
go build -ldflags "-X main.version=$version" -o tools/bin/mct-syslogger ./tools/mct-syslogger
./tools/bin/mct-syslogger --version
./tools/bin/mct-syslogger --help
./tools/bin/mct-syslogger --project example-project --instance-id example-instance --component example-component
```

`--version` 输出 `mct-syslogger <版本>` 后退出，无需 journalctl、运行时 Git 或管理员权限。Release 版本直接显示发布 tag；本地位于 tag 且工作区干净时也直接显示 tag，其余情况使用最近 tag、提交距离和短提交号，工作区有已跟踪改动时附加 `-dirty`，没有 tag 时使用短提交号。普通 `go build` 未注入版本时显示 `dev`。

可以在 shell 配置中添加别名（替换为实际仓库绝对路径）：

```sh
alias slog='/path/to/syslogger/tools/bin/mct-syslogger'
```

也可以直接复制二进制到 PATH 中的个人目录，作为独立命令使用：

```sh
mkdir -p "$HOME/.local/bin"
install -m 755 /path/to/syslogger/tools/bin/mct-syslogger "$HOME/.local/bin/mct-syslogger"
```

需要提升 journal 读取权限时，在命令前使用 `sudo`，例如 `sudo /path/to/syslogger/tools/bin/mct-syslogger -p example-project -i example-instance`。查询模式只读取日志和目录；主机配置通过独立的 `init` 子命令完成。`tools/bin/` 是已忽略的本地构建目录。

要让普通用户直接读取系统 journal，管理员可将其加入 `systemd-journal` 组（将 `username` 替换为实际用户名）：

```sh
sudo usermod -aG systemd-journal username
```

退出并重新登录后组权限生效。命令的 `--help` 提供查询流程和排障提示，账户权限由管理员按本节配置。

日志查询根据 `-l` 阈值、`--include-level` 和 `--exclude-level` 合并后的有效等级集合自动选择空间。只涉及一组时使用一个 `journalctl --namespace=<该组完整名称>` 精确选源；涉及多组时使用一个 `journalctl --namespace='*'`，以所选组的精确 `_NAMESPACE` 条件限定返回范围，由原生 journal 合并记录。身份、等级、时间及跟踪参数继续按下文组合。`-l error` 选择 error 组，`--include-level debug` 只选择 debug 组，info 与 notice 同选仍只打开 info 组；默认或 `-l debug` 表示全部八级，查询四组。各组独立管理容量、轮转和压缩；原生 `log` 兜底继续由宿主程序的 stderr／标准日志配置接管。SDK、CLI 和主机入口需配套升级，见[升级与手动清理](../docs/journal.md#升级与手动清理旧空间)。

## 初始化主机

```sh
sudo mct-syslogger init
sudo mct-syslogger init \
  --journal-debug-max-use 1G --journal-info-max-use 2G \
  --journal-warning-max-use 1G --journal-error-max-use 1G
mct-syslogger init --help
```

初始化需要 root、Linux、systemd 245+、已有本机 `app` 账户，以及 `systemctl`、`systemd-analyze`。独立子命令创建或核验由 `app` 及其主 GID 持有的名称根目录，权限为 `0755`。项目目录、JSON 和稳定锁由 SDK 创建，目录为 `0755`，文件为 `0644`，其他普通账户只读；root 过渡进程同样继承根目录的所有者。已有根目录的身份、权限、类型或 ACL 异常会明确报错，交管理员人工处理；已完成步骤保留。

四项参数分别控制 `mct-syslogger-debug/info/warning/error` 的持久化占用，notice 归 info，默认合计 5 GiB。配置开启原生透明压缩，查询自动解压，容量统计包含索引、元数据和压缩数据；`/run` 临时日志容量沿用系统现有配置。每次执行按本次参数应用配置并重启四个专用实例；省略某项使用其默认值，缩容可能触发对应空间清理旧归档，容量并非严格硬配额。SDK 负责路由，init 只检查 `MaxLevelStore` 不会过滤目标组日志，不生成该设置。

SDK、CLI 和主机入口使用配套版本。重复初始化保持已有名称子目录、数据及锁原状，管理员独立配置和默认系统 journal 保持原样；高优先级配置覆盖受管设置时报告真实来源并失败。245–247 使用受管命名空间主文件，248+ 使用受管 drop-in；历史配置由管理员手动处理。systemd 配置继续由 root 管理，日志读取权限仍单独管理。配置路径与应用步骤见[部署说明](../docs/journal.md)。

## 查看项目、实例与组件目录

业务开启 `Config.CatalogEnabled` 后，syslogger 向 `/var/lib/mct-syslogger` 发布目录。部署方准备该目录及接入应用写权限，并配套更新 SDK 和 CLI。`Config.InstanceDescription` 描述实例用途，组件通过 `Register(ComponentConfig)` 提供名称及 `Description`。目录开关默认关闭，与日志及指标出口独立。

```sh
# 查看全部项目、实例和组件。
slog --list

# 查看一个项目或一个实例。
slog --list -p example-project
slog --list -p example-project -i example-instance

# 按组件筛选，或输出机器可读的 JSON 数组。
slog --list -c example-component --colour
slog --list --json
```

树形输出示例（设备本地时间为 2026-11-03 14:40，时区为 `+08:00`）：

```text
mct-syslogger
├── example-project
│   └── example-instance  示例运行实例
│       ├── example-component    2026-11-03 14:38:15  每小时: 12:00=43 | 13:00=57 | 14:00=23
│       │                        示例组件描述
│       └── example-component-2  2026-11-03 14:38:16  每小时: 12:00=0 | 13:00=24 | 14:00=18
│                                另一个示例组件
└── example-project-2
    └── example-instance-2  另一个示例实例
        └── example-component    2026-11-03 14:37:00  每小时: 12:00=1 | 13:00=3 | 14:00=4
                                 示例组件描述
```

树形目录以 `mct-syslogger` 为统一展示根节点，其下依次为项目、实例和组件，并按名称排序，不增加表头。项目和实例保持单行展示，实例描述继续跟在名称后。组件首行依次显示名称、上次接收时间和三个自然小时的接收量；当前小时包含在内，如当前 14:40 时显示 12 点、13 点、14 点。所有选中组件按名称的终端显示宽度对齐时间列，组件描述另起一行与时间列对齐，空描述不占行。数量使用逗号千位分隔保留完整十进制，例如 `11,483`；最后活跃时间显示为 `YYYY-MM-DD HH:MM:SS`。小时起点通常只显示本地时分，三小时跨日期时补充月日，时区偏移变化时补充偏移，JSON 始终保留完整 UTC 起点。数量来自最近一次成功同步的目录记录，可能滞后约 10 分钟。

`--colour` 保留项目、实例及组件的原有身份配色；组件时间和完整的每小时计数沿用默认前景色并弱化亮度，组件描述与实例描述均使用终端默认前景色及正常亮度。控制字符继续转义，描述保持完整的单行文本，不按终端宽度截断或自动折行。目录排版使用 [uniseg v0.4.7](https://github.com/rivo/uniseg/tree/v0.4.7) 计算可见宽度，并使用 [go-humanize v1.1.0](https://github.com/dustin/go-humanize/tree/v1.1.0) 分组整数；Go 1.23 可用，分发许可见 [第三方许可](THIRD_PARTY_NOTICES.md)。

`-p`、`-i`、`-c` 使用原文精确匹配，同类重复取 OR，不同条件取 AND；指定组件筛选后只显示匹配组件及其所属实例。`--json` 输出实例对象数组，包含 `project`、`instance_id`、`instance_description`、`created_at` 和 `components`；每个组件包含 `name`、`description`、`created_at`、`last_active_at` 和 `log_counts`。`log_counts` 固定为从旧到新的三个小时项，格式为 `{"start_time":"2026-11-03T06:00:00Z","count":23}`；时间保持 UTC，计数为 `uint64` 整数。同一起点的增量相加，新起点标识独立时段，缺少活动的小时为零。数量固定按当前及前两个自然小时展示，与日志查询的 `-s/-u` 无关。

目录查询直接读取固定路径，与各进程的 `TMPDIR` 无关，也可在未安装 `journalctl` 时使用。目录尚未创建或筛选无结果时正常返回空列表；JSON 格式错误和实际文件读取失败明确报错。目录模式只接受身份筛选、`--json` 和 `--colour`，日志时间、等级、内容、条数和跟踪参数用于日志查询模式。

组件首次成功接收日志时由 SDK 后台建立记录，之后每 10 分钟批量合并小时增量，正常关闭时最终同步。每条日志按异步入队或同步开始发送的实际接收时间分桶，后续发送失败不回减数量；异常退出可能丢失最近一次成功同步后的增量。项目锁保护同身份多进程的按小时合并，旧批次晚落盘也会保留已写入的新小时。项目和实例文件名使用 SHA-256 摘要，JSON 保留身份原文。

SDK、CLI 和 JSON 消费方使用当前清单结构，未知字段及额外 JSON 对象会报错并保留源文件供人工处理。历史目录由部署方在停止写入后手动清理，再通过配套 init 和 SDK 建立当前资源。静默组件的旧小时数量在查询时归零，身份仍按一年活动期限保留。已有 `tools/bin/*` 二进制不会随源码自动更新。时区和时钟调整的边界见 [名称目录](../docs/catalog.md)。

组件按最后一次成功接收时间独立保留一年，后台轮询不续期。`--list` 只读过滤过期组件，过滤后没有组件的实例也不显示；SDK 每 10 分钟执行物理清理，并额外保留一个同步周期，让过期前已接收的活动继续合并历史计数，CLI 显示期限仍为一年。全部应用退出后暂停物理清理，后续运行再处理。名称目录可用于查询未过期的历史身份，实际日志仍按 journal 留存范围查询。名称目录按初始化后的固定所有者权限维护；缺失或不可写时，SDK 保留失败增量并重试，仅内置管理器健康降级，日志、业务组件及指标出口继续独立工作。目录历史数据和旧目录由部署方自行处理。

## 查看日志空间当前的时间范围

```sh
sudo mct-syslogger --ranges
sudo mct-syslogger --ranges --json
```

命令每次直接查询 debug、info/notice、warning、error 及以上四个空间当前可读记录的首尾时间。表格中的 `FIRST_ENTRY` 是最早记录时间，`LAST_ENTRY` 是最新记录时间，文字使用本机时区并显示 UTC 偏移；暂无可读记录时两个端点均为 `-`。例如：

```text
NAMESPACE              FIRST_ENTRY                LAST_ENTRY
mct-syslogger-debug    2026-11-06 15:20:00 +0800  2026-11-06 18:10:00 +0800
mct-syslogger-info     2026-11-06 13:00:00 +0800  2026-11-06 18:10:00 +0800
mct-syslogger-warning  -                          -
mct-syslogger-error    2026-11-06 14:00:00 +0800  2026-11-06 18:09:00 +0800
```

`--json` 输出按上述顺序排列的四项数组，每项包含 `namespace`、`first_entry`、`last_entry`。时间采用 UTC RFC3339 并保留微秒精度；空空间的两个时间为 `null`。边界来自 journal 收录时间，和普通查询 `-s/-u` 的口径一致，独立于日志正文的 `timestamp`、名称目录活动量及文件修改时间。

范围模式只接受 `--json`，按整个空间读取，不叠加身份、等级、时间、内容或条数筛选。只请求首尾记录的元数据，最早记录读取后立即结束对应子进程，兼容 systemd 245+，不遍历日志正文。查询失败明确报错并保留 journalctl 诊断，不输出部分概览。范围受当前账户读取权限约束，完整查看应使用 sudo 或具备完整 journal 读取权限的账户。

这些边界是查询当时的留存快照，不承诺区间内连续有日志，也不保证每个项目、实例或组件都有同样范围。首尾及不同空间分别读取，期间的新写入、轮转或清理可能使边界发生变化；重复执行会重新查询，不使用缓存。需要连续观察时可复用系统工具：

```sh
sudo watch -n 2 mct-syslogger --ranges
```

该模式只读，不改变容量、保留时间、轮转或主机配置。已有二进制需配套更新后才支持新参数。

## 查询示例

`mct-syslogger --help` 先展示逐层发现身份和常用查询，再给出参数速查、过滤组合、agent／脚本用法及排障提示。推荐先用 `--list` 核对身份，再查询日志；agent 可用 `--list --json` 获取实例对象数组，用日志查询的 `--json` 获取 NDJSON。

以下示例使用上方的 `slog` 别名：

```sh
# 某实例的某个组件，最近一小时内最新 100 条匹配日志。
slog -p example-project -i example-instance -c example-component

# 固定 UTC 时间范围内的全部匹配日志。
slog -p example-project -i example-instance -s '2026-10-23 09:00:00 UTC' -u '2026-10-23 10:00:00 UTC' -n 0

# 最近两小时，消息或业务字段同时包含两个关键词。
slog -p example-project -i example-instance -s=-2h -k '请求' -k '超时'

# 字面关键词忽略大小写；每个关键词都须在消息或业务字段中匹配。
slog -p example-project -i example-instance -k timeout -k retry --ignore-case

# Go 正则：匹配任一错误描述，忽略大小写。
slog -p example-project -c example-component -e '(?i)(timeout|connection refused)'

# 查看 info 及更严重的日志；debug 阈值表示全部等级。
slog -p example-project -i example-instance -l info

# 同时查看多个模块，精确选择 error 和 critical 两个等级。
slog -p example-project -c example-component -c example-component-2 --include-level error --include-level critical

# 排除 debug，或同时排除多个等级。
slog -p example-project -i example-instance --exclude-level debug
slog -p example-project -i example-instance --exclude-level debug --exclude-level info

# 组合阈值、精确包含和排除，最终仅显示 error。
slog -p example-project -l warning --include-level error --include-level critical --exclude-level critical

# 按日志等级及项目、实例、模块、消息、业务字段分色显示。
slog -p example-project -i example-instance --colour

# 从现在起实时跟踪；Ctrl+C 退出。
slog -p example-project -i example-instance -s now -f

# 从最近 10 分钟开始跟踪，匹配 20 条后退出。
slog -p example-project -i example-instance -s=-10m -f -n 20

# 每行输出完整 JSON，可继续用其他工具分析。
slog -p example-project -i example-instance -n 500 --json
```

## 过滤和输出规则

| 参数 | 含义 |
| --- | --- |
| `-p / --project PROJECT` | 先按 `SYSLOGGER_PROJECT` 原生索引预筛，再按 JSON `project` 原文精确匹配，可重复（OR） |
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
- 模块在 Linux 默认写入 `mct-syslogger` 专用原生 journal，保留 `SYSLOG_IDENTIFIER` 项目 tag、LOG_USER 和 `PRIORITY`，并以 `SYSLOGGER_PROJECT`、`SYSLOGGER_INSTANCE_ID`、`SYSLOGGER_COMPONENT` 保存原文身份。专用入口失败不自动转写默认 journal 或系统 syslog，原生 `log` 故障诊断保持现状。显式自定义 syslog socket 使用传统协议，JSON 正文保持一致；其他平台也需显式提供独立入口。
- 工具按原生身份字段与最终等级直接预筛，同字段重复匹配取 OR，不同字段取 AND。应用需升级 SDK 并重新部署，外部采集转发须保留三个 `SYSLOGGER_*` 身份字段；身份查询依赖这些字段。无需筛选身份时维持全范围读取；空等级集合仍保留 journalctl 的校验和错误反馈。
- 发送器与查询工具共享原生字段名，项目按原文匹配；Go 继续复核全部正文身份、等级和内容，避免不一致元数据混入结果。journalctl 保持不限原生条数，达到全部筛选后的匹配上限才停止读取，普通查询、跟踪、JSON 和输出顺序保持原契约。
- 查询先一次提取基础字段并组合判断项目、实例、组件和等级，再逐项检查 message；内容条件需要访问 fields 或可读输出需要显示它时，才展开业务字段。fields 的规范化 JSON 至多编码一次，供全部内容条件和显示复用；`--json` 保留原文，message 已满足全部内容条件时可跳过 fields 展开。整行语法和字段类型在输出与计数前校验，数字精度、字段键排序和转义规则保持一致。
- journal 身份、等级预筛减少传入 Go 的无关正文，多级按需处理继续减少解析、对象分配和编码开销；缩小时间范围可进一步减少候选记录，实际总耗时也受 journal 索引、扫描及 I/O 影响。Go 1.23 已退出官方常规支持窗口，部署方需关注工具链安全维护。
- 可读输出包含时间、等级、`project/instance_id/component`、消息及业务字段，换行和控制字符转义为单行。JSON 输出每行一条记录，不包含 journal 外层元数据。
- 彩色输出中，时间及分隔符使用终端默认前景色和亮度，`project` 使用紫色、`instance_id` 使用蓝色、`component` 使用青色、`message` 使用绿色；`fields` 沿用终端默认前景色并弱化亮度（ANSI 2），通过明暗与时间区分，实际效果由终端主题和样式支持决定。等级配色为：`emergency` 红底加粗白字、`alert` 加粗亮紫、`critical` 亮红、`error` 红、`warning` 黄、`notice` 青、`info` 绿、`debug` 灰。
- `--colour` 输出 ANSI 转义序列，适用于支持 ANSI 颜色的终端，重定向时也保留颜色序列。纯文本输出使用默认设置或 `--colour=false`；`--json` 始终保留原始 JSON 正文。
- 无匹配结果正常返回 `0`；参数或正则错误返回 `2`，查询失败返回 `1`，Ctrl+C 返回 `130`。journalctl 的权限或时间格式错误属于查询失败，返回 `1`，诊断保留在标准错误输出中；可见日志范围取决于设备权限与留存策略。
- 查询结果写入 stdout，帮助及诊断写入 stderr。无结果时，先扩大时间范围，再核对身份和组合条件，最后检查 journal 权限及留存范围；名称目录提供历史身份线索，进程运行情况需结合实际日志判断。

## 查询性能诊断

在原有查询命令后添加 `--debug`，日志仍写入 stdout，性能汇总写入 stderr：

```sh
mct-syslogger -p example-project -i example-instance -l notice --debug

# 单独保存性能汇总和 journalctl 诊断。
mct-syslogger -p example-project -i example-instance -l notice --debug 2>query-debug.txt
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
go test ./tools/install
go vet ./tools/mct-syslogger ./internal/catalog
GOTOOLCHAIN=go1.23.12 go test -run '^$' -bench '^(BenchmarkQueryRecord|BenchmarkFilterLogs)$' -benchmem ./tools/mct-syslogger
```

测试使用内存日志、隔离目录和测试辅助进程，覆盖目录发布与读取、筛选和输出，以及日志解析、数量限制、跟踪参数及收尾，不读取真实系统日志。JSON 差分测试和 fuzz 使用优化前标准库实现作为对照，覆盖重复键合并、空值、非法内容、精度、转义、按需展开及缓存所有权；基准分别测量单条 CPU 路径和含行读取、缓存反转、写入的完整过滤路径。原生预筛测试使用 SDK 编码的正文与元数据，覆盖等级阈值、包含/排除、多项目、tag 归一化碰撞、实例/组件/内容复核及匹配后限量。性能诊断测试覆盖分级计数、字段复用、读取/写入归属、stderr 分流、取消和错误；`BenchmarkFilterDebug` 对照开启与关闭诊断时的处理开销。

安装脚本测试在 Linux 使用 util-linux 的 `script` 创建控制终端，通过下载和提权命令替身及临时安装前缀，验证管道交互、校验、安装、初始化参数和失败收尾；测试不调用真实主机初始化，也不写入系统安装目录。
