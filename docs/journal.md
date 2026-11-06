# 专用 journal 配置

## 日志入口与存储

SDK 按真实日志等级将每条结构化记录交给一个专用 journal 命名空间。四个空间由独立的 journald 实例管理，各自拥有日志文件、容量及轮转策略：

| namespace | 等级 | init 容量参数 | 默认值 |
| --- | --- | --- | --- |
| `mct-syslogger-debug` | debug | `--journal-debug-max-use` | `1G` |
| `mct-syslogger-info` | info、notice | `--journal-info-max-use` | `2G` |
| `mct-syslogger-warning` | warning | `--journal-warning-max-use` | `1G` |
| `mct-syslogger-error` | error、critical、alert、emergency | `--journal-error-max-use` | `1G` |

原生 socket 为 `/run/systemd/journal.<namespace>/socket`，持久化数据通常位于 `/var/log/journal/<machine-id>.<namespace>/`；内存存储使用 `/run/log/journal/` 下对应目录。各组内的项目、实例和组件共享容量。四组默认合计 5 GiB，仍共享底层磁盘、CPU、IO 和可用空间；CLI 保留原身份、等级和 journal 原生索引。

标准库 `log` 是独立故障诊断及无效组件句柄的兜底通道，保留宿主程序配置，默认写入 stderr；被 systemd 等宿主收集后仍可能出现在默认系统日志中。常规结构化日志通过专用入口发送，专用入口不可用时不会自动改投默认 journal 或系统 syslog。

## 主机准备

需要支持 journal namespaces 的 Linux 主机及 systemd 245+。管理员使用独立的初始化子命令准备主机，SDK 和日常查询只使用已经准备的入口：

```sh
sudo mct-syslogger init --user appuser
# 可独立设置四项；省略的项目使用各自默认值。
sudo mct-syslogger init --user appuser \
  --journal-debug-max-use 1G \
  --journal-info-max-use 2G \
  --journal-warning-max-use 1G \
  --journal-error-max-use 1G
mct-syslogger init --help
```

`appuser` 必须是已有本机账户，root-only 部署可以指定 `--user root`。命令需要 root 及系统工具 GNU `getent`、`groupadd`、`usermod`、`systemctl`、`systemd-analyze`。账户和写入组由本机 files 管理，名称及 UID/GID 与当前 NSS 解析保持一致；默认 `files systemd` 可用。工具核验可枚举的主组、同 GID 别名组及附加成员，并确认目标账户的有效组身份；不可枚举外部身份源、自定义 userdb 或管理员额外服务组授权由部署方单独核验，初始化不管理这些授权域。

命令同时准备 `/var/lib/mct-syslogger` 的共享组写权限：root 与指定账户可写，其他本机账户只读。先停止旧版写入进程再迁移权限，完成后重新登录或重启业务服务刷新组身份；额外组成员、ACL 和异常受管对象会报错，交管理员先处理。

### 初始化过程输出

`init` 按执行顺序向标准输出报告实际操作，最后保留完成摘要：

- **命令**：执行程序的实际路径和独立参数，包括身份检查、组管理及 systemd 操作。命令在执行前记录，执行失败继续通过原有错误返回反馈；账户查询结果和配置正文不会作为过程日志倾倒。
- **目录**：区分创建与复用，列出根目录、项目目录及配置目录的实际路径。
- **权限**：在属主／属组、权限分别设置成功后记录对象路径、UID/GID 及 Unix 权限值。重复执行仍报告设置动作，不把相同值描述成新增变更。
- **配置**：原子替换成功后区分创建与更新；只有旧受管文件实际删除后才报告清理，管理员文件明确报告保留。文件内容及稳定锁 inode 的迁移规则保持原样。
- **systemd 结果**：保留 `add-wants` 返回的链接创建信息，检查通过后报告专用 socket 就绪；由 systemd 管理的运行目录和 journal 文件不假报为 `init` 直接创建。

以下为已有目录、GID 为 986、systemd 248+ 时的节选示例，具体命令路径和对象取决于主机：

```text
[开始初始化] 运行账户="app"
[执行命令] "/usr/bin/systemd-analyze" 参数=["--version"]
[等级空间] namespace="mct-syslogger-debug" 持久化容量="1G"
[复用目录] "/var/lib/mct-syslogger"
[设置目录属主] "/var/lib/mct-syslogger" UID=0 GID=986
[设置目录权限] "/var/lib/mct-syslogger" mode=2775
[更新配置] "/etc/systemd/journald@mct-syslogger-debug.conf.d/60-mct-syslogger-init.conf" mode=0644（临时文件原子替换）
[执行命令] "/usr/bin/systemctl" 参数=["restart" "systemd-journald@mct-syslogger-debug.service"]
```

过程信息用于查看执行进度，不是只读预演。中断或失败时，之前已完成的操作保留；最终摘要仅在所有步骤成功后输出。

### 配置与应用

systemd 248+ 时，每组管理 `/etc/systemd/journald@<namespace>.conf.d/60-mct-syslogger-init.conf`；245–247 的命名空间尚不加载 drop-in，改为管理带工具标识的 `/etc/systemd/journald@<namespace>.conf`。主文件搜索保留 256+ 的目录优先级规则。已有非受管目标文件会明确报错并保留，需管理员先调整。debug 组默认内容如下：

```ini
[Journal]
Storage=persistent
SystemMaxUse=1G
Compress=yes
ForwardToSyslog=no
ForwardToKMsg=no
ForwardToConsole=no
ForwardToWall=no
```

四项容量均接受正整数字节及 `K/M/G/T/P/E` 单位，按 1024 进制计量。`RuntimeMaxUse` 保持系统现有配置。原生透明压缩以数据对象为单位，查询自动解压；容量包含索引、元数据和压缩后数据，按归档文件轮转清理，不是逐字节硬配额。四个转发选项将日志流保留在对应实例；另有 `ForwardToSocket` 或采集器时，管理员需核对下游目标。

分组由 SDK 路由完成，init 不写入 `MaxLevelStore`。未配置时使用 journald 默认 debug；显式设置须允许该组完整等级，debug/info/warning/error 的最大 severity 分别为 7/6/4/3。有效阈值过严、空赋值或无法确认的值会报告来源并失败，避免静默丢弃日志。其他限流及留存设置继续沿用主机配置。

初始化只替换自身标识的受管文件，保留管理员独立配置。命令按已安装 systemd 版本选择主文件与 drop-in 的实际加载规则，逐文件解析；正文路径注释不作为文件边界，BOM 可读取，语法损坏明确失败。高优先级片段覆盖受管设置时，报告冲突键和真实来源，由管理员先调整。版本切换时，先写入并核验新受管配置，再清理工具自己生成的旧非活动文件，保留管理员文件。

初始化分步骤执行，失败时已完成步骤保留，修正原因后可重试；每次省略某项容量都使用该项默认值，缩小容量可能使对应空间清理旧归档。

初始化对四组分别加入开机启动依赖并立即启动，以 warning 组为例：

```sh
sudo systemctl add-wants sockets.target systemd-journald@mct-syslogger-warning.socket
sudo systemctl start systemd-journald@mct-syslogger-warning.socket
```

`add-wants` 为实例 socket 建立持久的目标依赖，适用于模板没有 `[Install]` 段的发行版。每次初始化随后重启专用实例，使上次在配置写入后、应用前中断的情况也可通过重复执行恢复：

```sh
sudo systemctl restart systemd-journald@mct-syslogger-warning.service
```

检查入口及存储：

```sh
systemctl status systemd-journald@mct-syslogger-warning.socket
sudo journalctl --namespace=mct-syslogger-warning --disk-usage
```

业务使用默认 `Config` 日志出口即可，保持 `SyslogSocketPath` 为空。SDK 直接连接专用 socket，业务服务的 stdout／stderr 仍由原宿主管理；无需给整个业务服务设置 `LogNamespace=`。若业务要求启动时入口已经就绪，可在其 systemd 服务的 `[Unit]` 中添加：

```ini
Requires=systemd-journald@mct-syslogger-debug.socket systemd-journald@mct-syslogger-info.socket systemd-journald@mct-syslogger-warning.socket systemd-journald@mct-syslogger-error.socket
After=systemd-journald@mct-syslogger-debug.socket systemd-journald@mct-syslogger-info.socket systemd-journald@mct-syslogger-warning.socket systemd-journald@mct-syslogger-error.socket
```

## 查看日志

```sh
# 单组日志，info 组同时包含 notice。
sudo journalctl --namespace=mct-syslogger-info
sudo journalctl --namespace=mct-syslogger-error SYSLOGGER_PROJECT=contract-listener

# CLI 根据等级条件选取对应空间，统一查询与跟踪。
mct-syslogger -p contract-listener -i mainnet-a -c rpc
mct-syslogger -p contract-listener -s now -f
```

CLI 使用 `journalctl --namespace='*'` 加所选组的精确 `_NAMESPACE` 条件，由原生 journal 合并记录。通配入口会枚举可访问的 namespace，匹配条件限定返回范围。普通 `journalctl` 显示默认命名空间；读取权限沿用系统 journal 的组和 ACL 配置，namespace 用于日志流及留存隔离，不是管理员权限隔离。

先完成四个空间的 init，再启动查询与跟踪。本机 systemd 257 验证中，跟踪启动后才新增的整个 namespace 不会自动纳入；如期间创建或重建了 namespace 目录，应重新启动跟踪。已准备空间内的新日志和正常文件轮转继续由原生监听处理。

## 排障

- SDK 继续在实际发送时连接日志服务。`New`／`SafeNew` 成功表示管理器创建完成，不表示 journald 已经可用。连接失败沿用原生 `log` 故障诊断、发送失败计数、失败条目放弃及后续日志重连行为；同步方法直接返回发送错误。
- `SyslogSocketPath` 是传统 syslog 协议选项，适用于显式部署的独立 syslog 服务和隔离测试。原生 journal socket 不能填入该选项；指定自定义路径时，由接入方保证其留存及转发目标独立。
- 非 Linux 平台的默认发送返回不支持错误，显式传统 syslog 入口继续可用。名称目录和指标出口维持各自的平台及配置契约。

## 升级与手动清理旧空间

四空间版本直接切换写入和查询返回范围。旧 `mct-syslogger` 空间的记录不会出现在新 CLI 中；程序不提供旧空间写入、历史查询、双写或自动迁移和清理入口。journalctl 的通配入口仍会枚举可访问的 namespace，返回记录以四空间精确条件限定。旧 `--journal-max-use` 参数由四项独立容量参数替换，旧调用按未识别参数报错。

先运行新 init 准备四个入口，再用新 SDK **重新编译、部署并重启所有业务程序**，最后使用新 CLI 核对八级日志分布。更新命令工具不会更新业务二进制中已嵌入的 Go module。旧空间保留期间额外占用磁盘，四组预算不包括旧数据；名称目录权限迁移仍需先停止旧写入进程。

确认所有旧业务已退出、四组日志正常且旧历史无需保留后，由管理员手动执行下面的清理。**删除日志不可恢复；只处理精确的旧实例名称，不用 `mct-syslogger*` 通配删除。保留四个新空间、默认系统 journal、共享 `/var/lib/mct-syslogger` 及写入组。**

### 1. 盘点旧实例的实际激活入口与配置

```sh
systemctl show systemd-journald@mct-syslogger.service -p TriggeredBy -p FragmentPath -p DropInPaths
systemctl cat systemd-journald@mct-syslogger.socket systemd-journald@mct-syslogger.service
systemctl list-dependencies --reverse systemd-journald@mct-syslogger.socket
```

只移除旧实例专属的配置和依赖，保留发行版提供的 `@.service`、`@.socket` 模板。若 `TriggeredBy` 列出其他旧实例专属入口（例如 varlink socket），同时盘点并停止它；其名称和开机依赖以当前发行版实际结果为准。检查旧实例在 `/etc/systemd`、`/run/systemd`、`/usr/local/lib/systemd`、`/usr/lib/systemd` 和 `/lib/systemd` 中的独立配置、同名 drop-in 及管理员自定义 unit drop-in，不只检查工具受管片段。

### 2. 停止旧入口与服务，移除专属配置

常见部署的两个旧单元：

```sh
sudo systemctl stop systemd-journald@mct-syslogger.socket systemd-journald@mct-syslogger.service
```

同时停止步骤 1 查到的其他旧实例专属激活入口。核对并移除旧 `add-wants` 创建的链接及工具配置（不存在的路径跳过）：

```sh
sudo rm -f -- /etc/systemd/system/sockets.target.wants/systemd-journald@mct-syslogger.socket
sudo rm -f -- /etc/systemd/journald@mct-syslogger.conf
sudo rm -f -- /etc/systemd/journald@mct-syslogger.conf.d/60-mct-syslogger-init.conf
```

上述路径应先确认只属于旧实例；对于同名管理员配置，应在盘点后决定移除。对其他目录下的旧实例配置、激活依赖或 unit drop-in，同样逐项核对后手动移除；空配置目录可用 `rmdir` 删除。业务 unit 中若仍引用旧 socket，也须改为四个新入口，然后重新加载：

```sh
sudo systemctl daemon-reload
systemctl is-active systemd-journald@mct-syslogger.socket systemd-journald@mct-syslogger.service
```

确认两个旧单元以及其他旧实例激活入口均不处于 active 状态后，再删日志。清理失败时先定位残留依赖，不能一边继续写入一边删除。

### 3. 核对并删除旧日志目录

以下命令在同一个 shell 中逐条执行。先核对显示的路径是旧 namespace，而不是四组之一：

```sh
machine_id=$(cat /etc/machine-id)
printf '%s\n' "/var/log/journal/${machine_id}.mct-syslogger" "/run/log/journal/${machine_id}.mct-syslogger"
sudo ls -ld -- "/var/log/journal/${machine_id}.mct-syslogger" "/run/log/journal/${machine_id}.mct-syslogger"
```

只删除实际存在并已确认无需保留的目录：

```sh
sudo rm -r -- "/var/log/journal/${machine_id}.mct-syslogger"
sudo rm -r -- "/run/log/journal/${machine_id}.mct-syslogger"
```

若主机曾变更 machine-id，逐个核对其他旧 namespace 目录，再以完整路径处理，不使用前缀通配。服务停止后残留的 `/run/systemd/journal.mct-syslogger` 也可按完整路径核对后删除；不得删除共享 `/run/systemd/journal`。

最后确认四组仍正常接收日志、旧实例未重新激活。若旧目录被重新创建，先查旧业务二进制、启动依赖或配置管理任务，不反复删目录掩盖仍在写入的问题。

官方参考：[journal namespaces](https://www.freedesktop.org/software/systemd/man/latest/systemd-journald.service.html#Journal%20Namespaces)、[journald.conf](https://www.freedesktop.org/software/systemd/man/latest/journald.conf.html)、[journalctl](https://www.freedesktop.org/software/systemd/man/latest/journalctl.html)。
