# syslogger

Go 1.19 标准库实现的本机 syslog 日志与组件健康状态库。module 为 `github.com/mctxyz/syslogger`，包名为 `syslogger`。

```text
日志 → 模块独立 FIFO → 本机 syslog → 设备保存、轮转、转发
状态 → 同步内存记录 → HTTP／.prom → Prometheus → Alertmanager
```

## 身份与接入

```go
import "github.com/mctxyz/syslogger"
```

日志和指标共用 `project、instance_id、component`。业务代码固定项目名和组件名，业务入口从配置文件等来源读取实例 ID 后传给库；空实例 ID 使用 `common`，显式非空值按 3～128 字节、UTF-8 和标签上限校验并保留原文。

下方为初始化片段，业务在 New 成功后接管管理器的关闭职责，后续初始化失败也按自己的流程调用 Close 收尾。

```go
manager, err := syslogger.New(syslogger.Config{
    Project: "contract-listener",
    InstanceID: instanceID,
    StateEnabled: true,
    HTTP: syslogger.HTTPConfig{Enabled: true, ListenAddr: ":9091"},
})
if err != nil { return err }
rpc, err := manager.Register("rpc", 512)
if err != nil { return err }
if err := manager.StartHTTP(); err != nil { return err }
rpc.Unhealthy()
```

已有服务器通过 `Handler()` 挂载，服务器归业务管理。模块在取得 Handler 或开始启动指标出口之前完成注册；配置及采集集合之后固定。模块注册后日志即可使用，业务退出时调用 `Close(ctx)`。

同一项目的并行实例应显式提供不同 ID，跨重启复用。多个实例使用 common 会共享逻辑身份；各实例的指标文件路径由接入方分别配置。

### 容错初始化

`New(Config) (*Manager, error)` 严格校验配置。需要在配置异常时继续运行，可使用 `SafeNew(Config) *Manager`：

```go
manager := syslogger.SafeNew(syslogger.Config{
    Project: "contract-listener",
    InstanceID: instanceID,
    StateEnabled: true,
})
```

`SafeNew` 使用纠正后的配置副本创建完整的管理器，组件注册、日志提交、状态维护及关闭流程与 `New` 共用实际实现。后续 `Register`、`Handler`、`StartHTTP`、`StartFile` 和 `Close` 继续按各自契约返回错误，例如出口关闭、端口占用、目录不存在或管理器已关闭。

| 配置场景 | 有效配置及诊断 |
| --- | --- |
| `Project` 为空字符串 | 使用 `common`，记录 Warning |
| `Project` 非空但不合法 | 使用 `unknown`，记录 Warning |
| `InstanceID` 为空字符串 | 正常使用默认值 `common` |
| `InstanceID` 非空但不合法 | 使用 `common`，记录 Warning |
| `MaxLabelValueBytes` 为零 | 正常使用默认值 2048 |
| `MaxLabelValueBytes` 非零且小于 21 | 回退 2048，记录 Warning |
| 指标文件已启用，但 `OutputPath` 为空或全空白 | 停用指标文件出口，记录 Error；HTTP 和 syslog 配置保持原样 |
| 指标文件已启用，刷新间隔为负 | 回退 10 秒，记录 Warning |
| 指标文件已启用，刷新间隔为零 | 正常使用默认值 10 秒 |
| 纠正后仍有指标出口启用，但状态维护关闭 | 自动开启 `StateEnabled`，记录 Warning |

合法身份保留原文，多个非法身份可能共享同一回退身份。已关闭的指标文件配置沿用原有处理语义；状态依赖根据纠正后仍启用的指标出口补齐。

每项异常首先由标准库 `log.Printf` 输出，默认写入 stderr，接入方可通过标准日志配置调整目标。库同时通过独立的初始化诊断队列，尝试以 `component="syslogger_manager"` 交付结构化 Warning／Error syslog 日志，业务字段包含 `state="unhealthy"`、字段名、原因和纠正结果。该队列由管理器关闭，补充诊断交付失败通过标准日志反馈。

开启状态维护时，发生配置纠正的管理器持续显示 `Unhealthy`（3），后续组件注册成功保持该配置异常事实。正常采用默认值保持正常初始化状态；HTTP、指标文件仍按原有方式显式接入或启动，系统日志留存及外部资源可用性由对应环境决定。

## 日志

八级方法为 `Emergency、Alert、Critical、Error、Warning、Notice、Info、Debug`，对应原生 severity 0～7。方法接收 `(message string, fields map[string]any) error`，字段可传 nil；八级事件统一提交，等级过滤交由设备日志服务配置，进程退出由业务管理。

```json
{"timestamp":"2026-10-22T08:00:00Z","level":"error","project":"contract-listener","instance_id":"common","component":"rpc","message":"RPC 请求超时","fields":{"error":"context deadline exceeded","duration_ms":3000}}
```

正文实际为单行 JSON，时间为 UTC RFC3339Nano，等级为小写名称。顶层 error 字段使用 Error() 文本，nil 或空字段省略 fields。编码在调用线程完成，调用期间字段由业务保证无并发修改；返回后修改原字段不会改变已编码日志。协议头使用 LOG_USER facility 和真实 severity，syslog tag 来自项目名；协议头中的空白和控制字符规范化为下划线，JSON 身份保留原文。

每模块拥有有缓冲 channel 组成的独立指针 FIFO 和固定发送任务。`Register(name, capacity)` 两参数必传，1～4096 使用原值，其余回退 512。队满立即拒收新日志并返回错误，已接收记录保持 FIFO。发送成功只表示本机 socket 写入成功，中心接收和持久化由设备日志链路管理。

拒收发生在编码或入队阶段，由调用返回 error；发送失败发生在后台连接或写入阶段，通过独立诊断和 Counter 反馈，失败条目即放弃。连接恢复服务后续日志，不重放失败记录。本机连接及写入等待各受有限超时约束，业务前台仍承担编码成本，库不承诺硬实时调用上限。

`SyslogSocketPath` 可指定部署或隔离测试的本机路径；默认尝试 `/dev/log`、`/var/run/syslog`、`/var/run/log` 的 Unix datagram 和 stream。设备负责实际留存、过滤、限流及中心转发，提交 Debug 不保证最终可查询。

### 日志检索

[tools/syslogger-log](tools/syslogger-log) 提供独立 Go 命令，支持按项目、实例、模块、等级、时间、关键词和正则过滤，以及实时跟踪、彩色输出和 JSON 输出：

```sh
go build -o tools/bin/syslogger-log ./tools/syslogger-log
./tools/bin/syslogger-log -p contract-listener -I local-a -c rpc --since=-2h -k '超时'
```

编译使用 Go 1.19+，运行环境为 Linux 和 journalctl。用法与快捷命令设置见 [工具说明](tools/README.md)。

## 健康与指标

`Healthy()`、`Degraded()`、`Unhealthy()` 无参数、无返回值，同步提交状态及库采集的时间。状态维护默认关闭，关闭时这些方法直接返回；日志与状态分别调用。四态为 0=Unknown、1=Healthy、2=Degraded、3=Unhealthy，Unknown 表示业务组件尚未汇报。较新入口时间整体替换记录，相同时间由后提交者覆盖，同状态也更新时间。

每个组件独立读写锁，采集复制单组件完整记录后释放锁，输出 I/O 在锁外执行。内置组件 `syslogger_manager` 表达管理器初始化、注册失败及实际注册恢复，具体来源走独立诊断。

| 指标 | 类型 | 含义 |
| --- | --- | --- |
| `syslogger_component_state` | Gauge | 当前四态数值 |
| `syslogger_component_timestamp_seconds` | Gauge | 当前健康记录的 Unix 整数秒，业务组件未汇报时为 0 |
| `syslogger_component_log_rejected_total` | Counter | 模块入队前拒收的调用累计次数 |
| `syslogger_component_log_send_failures_total` | Counter | 模块后台发送失败的条目累计数 |

全部样本只带三个身份标签。业务组件输出四条样本，内置管理器输出状态和时间。两个 Counter 从 0 累计，健康恢复不清零，管理器重建后归零；关闭期限到达时尚未尝试的积压通过 Close 错误反馈。

HTTP 与文件出口默认关闭，启用时须同时开启 StateEnabled。`StartHTTP()` 同步绑定后后台提供 `/metrics`。`StartFile()` 首次同步写入完整快照，成功后周期刷新，周期默认 10 秒。两个出口各自启动，失败可显式再次尝试，重复启动返回错误。

文件出口通过 `Config.File.OutputPath` 指定完整路径及文件名，父目录由接入方准备；相对路径基于当前工作目录。例如：

```go
fileConfig := syslogger.FileConfig{
    Enabled: true,
    OutputPath: "/var/lib/node_exporter/textfile_collector/listener-a.prom",
}
```

将该配置赋给 `Config.File`。库按给定路径交付文件；不同实例应配置不同路径，同一路径的写入会互相覆盖。供 node_exporter 采集的文件应使用 `.prom` 扩展名。

写入沿用同目录临时文件、0644、完整关闭后 Rename，失败保留旧完整文件。汇报时间不是文件刷新时间，文件新鲜度结合 node_exporter 的 textfile mtime 解释；同时采集 HTTP 和文件时应避免重复聚合。

### 现有采集目录迁移

切换到指定路径后，已有的哈希命名指标文件仍会保留。接入方应按精确实例及文件路径核对旧文件，并在确认后移出采集目录，避免新旧文件重复采集。

## 关闭与验证

首次 `Close(ctx)` 建立接收截止，并以该上下文限制日志排空；到期取消连接、释放积压记录并反馈未完成交付。只关闭库拥有的 HTTP 服务器，外部服务持有的 Handler 随后返回 503。已成功启动的文件出口串行写入最终快照并保留文件，未启动的出口保持原状态。重复 Close 复用收尾结果，后续 ctx 只限制本次等待。文件系统和独立错误输出的底层 I/O 不承诺硬实时可取消。

```sh
go test ./...
go vet ./...
go test -race ./...
```

测试使用独立临时 socket 和目录，不写真实系统日志；环境中有 promtool 时自动检查实际编码的指标样本。真实 journald／rsyslog、日志中心、Prometheus／Alertmanager 联调和业务接入按对应环境授权另行验收。
