# syslogger

Go 1.19 本机 syslog 日志与组件健康状态库，指标注册和导出使用 Prometheus 官方客户端。module 为 `github.com/mctxyz/syslogger`，包名为 `syslogger`。

```text
日志 → 模块独立 FIFO → 本机 syslog → 设备保存、轮转、转发
状态 → 同步内存记录 → HTTP／.prom → Prometheus → Alertmanager
```

## 身份与接入

```go
import "github.com/mctxyz/syslogger"
```

日志和内置指标共用 `project、instance_id、component`。业务代码固定项目名和组件名，二者保留非空白且合法 UTF-8 的原文；业务入口从配置文件等来源读取实例 ID 后传给库，空实例 ID 使用 `common`，显式非空值按 3～128 字节和 UTF-8 校验并保留原文。

下方为初始化片段，业务在 New 成功后接管管理器的关闭职责，后续初始化失败也按自己的流程调用 Close 收尾。

```go
manager, err := syslogger.New(syslogger.Config{
    Project: "contract-listener",
    InstanceID: instanceID,
    StateEnabled: true,
    HTTP: syslogger.HTTPConfig{Enabled: true, ListenAddr: ":9091"},
})
if err != nil {
    return err
}
rpc, err := manager.Register(syslogger.ComponentConfig{
    Name: "rpc",
    Description: "区块与日志获取",
    QueueCapacity: 512,
})
if err != nil {
    return err
}
if err := manager.StartHTTP(); err != nil {
    return err
}
rpc.Unhealthy()
```

已有服务器通过 `Handler()` 挂载，服务器归业务管理。模块在取得 Handler、开始启动指标出口或首次通过 Registry 采集之前完成注册；配置及组件集合之后固定。模块注册后日志即可使用，业务退出时调用 `Close(ctx)`。

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
| `Registry` 注册内置 Collector 失败 | 使用独立 Registry，记录 Error；调用方原注册表保持原样 |
| `DefaultQueueCapacity` 为零 | 正常使用默认值 512 |
| `DefaultQueueCapacity` 为负或大于 4096 | 回退 512，记录 Warning |
| 指标文件已启用，但 `OutputPath` 为空或全空白 | 停用指标文件出口，记录 Error；HTTP 和 syslog 配置保持原样 |
| 指标文件已启用，刷新间隔为负 | 回退 10 秒，记录 Warning |
| 指标文件已启用，刷新间隔为零 | 正常使用默认值 10 秒 |
| 队满自动降级已启用，`QueueFullRecoveryInterval` 为负 | 回退 30 秒，记录 Warning |
| 队满自动降级已启用，`QueueFullRecoveryInterval` 为零 | 正常使用默认值 30 秒 |
| 纠正后仍有指标出口或队满自动降级启用，但状态维护关闭 | 自动开启 `StateEnabled`，记录 Warning |

合法身份保留原文，多个非法身份可能共享同一回退身份。已关闭的指标文件配置沿用原有处理语义；状态依赖根据纠正后仍启用的指标出口和队满自动降级功能补齐。

每项异常首先由标准库 `log.Printf` 输出，默认写入 stderr，接入方可通过标准日志配置调整目标。库同时通过独立的初始化诊断队列，尝试以 `component="syslogger_manager"` 交付结构化 Warning／Error syslog 日志，业务字段包含 `state="unhealthy"`、字段名、原因和纠正结果。该队列由管理器关闭，补充诊断交付失败通过标准日志反馈。

开启状态维护时，发生配置纠正的管理器持续显示 `Unhealthy`（3），后续组件注册成功保持该配置异常事实。正常采用默认值保持正常初始化状态；HTTP、指标文件仍按原有方式显式接入或启动，系统日志留存及外部资源可用性由对应环境决定。

## 日志

八级方法为 `Emergency、Alert、Critical、Error、Warning、Notice、Info、Debug`，对应原生 severity 0～7。方法接收 `(message string, fields Fields)`，无返回值，字段可传 nil。`Fields` 是以 `map[string]any` 为底层类型的结构化业务字段类型，调用方使用 `syslogger.Fields` 构造字段；八级事件统一提交，等级过滤交由设备日志服务配置，进程退出由业务管理。

```go
rpc.Info("RPC 已连接", syslogger.Fields{
    "endpoint": "primary",
})
rpc.Healthy()
```

```json
{"timestamp":"2026-10-22T08:00:00Z","level":"error","project":"contract-listener","instance_id":"common","component":"rpc","message":"RPC 请求超时","fields":{"error":"context deadline exceeded","duration_ms":3000}}
```

正文实际为单行 JSON，时间为 UTC RFC3339Nano，等级为小写名称。顶层 error 字段使用 Error() 文本，nil 或空字段省略 fields。编码在调用线程完成，调用期间字段由业务保证无并发修改；返回后修改原字段不会改变已编码日志。协议头使用 LOG_USER facility 和真实 severity，syslog tag 来自项目名；协议头中的空白和控制字符规范化为下划线，JSON 身份保留原文。

每模块拥有有缓冲 channel 组成的独立指针 FIFO 和固定发送任务。`Config.DefaultQueueCapacity` 为管理器设置模块默认容量，零值为 512，合法范围为 1～4096，`New` 和 `SafeNew` 均可配置。`New` 拒绝非法默认容量，`SafeNew` 回退 512 并记录配置纠正。

`Register(ComponentConfig)` 按 `Name` 建立模块，`Description` 用于日志名称目录展示。`QueueCapacity` 为 1～4096 时使用原值，零值及其他值采用管理器默认容量。例如：

```go
manager := syslogger.SafeNew(syslogger.Config{
    Project: "demo",
    DefaultQueueCapacity: 1024,
})
rpc, err := manager.Register(syslogger.ComponentConfig{
    Name: "rpc",
    Description: "区块与日志获取",
})
if err != nil {
    return err
}
rpc.Info("组件已注册", nil)
```

队满立即拒收新日志，更新拒收 Counter 并通过标准日志诊断，已接收记录保持 FIFO。发送成功只表示本机 socket 写入成功，中心接收和持久化由设备日志链路管理。

编码失败、队满或管理器关闭后的日志调用通过标准库 `log.Printf` 输出独立诊断，并累计模块拒收次数；提交诊断按模块每秒最多输出一次，Counter 保留全部次数。后台连接或写入失败沿用独立标准日志诊断和发送失败 Counter，失败条目即放弃。连接恢复服务后续日志，不重放失败记录。本机连接及写入等待各受有限超时约束，业务前台仍承担编码和必要的标准日志输出成本，库不承诺硬实时调用上限。

空组件句柄及缺少管理器的组件通过标准日志输出原有句柄警告，下一行输出采用相同事件格式的单行 JSON，保留等级、时间、消息及业务字段。此时项目和实例身份使用空字符串，组件名称存在时保留；警告与事件使用同一次 `log.Printf` 输出。业务字段编码失败时保留句柄警告，并在下一行输出编码失败原因。

`SyslogSocketPath` 可指定部署或隔离测试的本机路径；默认尝试 `/dev/log`、`/var/run/syslog`、`/var/run/log` 的 Unix datagram 和 stream。设备负责实际留存、过滤、限流及中心转发，提交 Debug 不保证最终可查询。

### 日志检索

[tools/mct-syslogger](tools/mct-syslogger) 提供独立 Go 命令，支持项目／实例／组件目录、日志过滤、实时跟踪、彩色输出和 JSON 输出：

```sh
go build -o tools/bin/mct-syslogger ./tools/mct-syslogger
./tools/bin/mct-syslogger --list
./tools/bin/mct-syslogger -p contract-listener -i local-a -c rpc -s=-2h -k '超时'
```

编译使用 Go 1.19+，运行环境为 Linux 和 journalctl。用法与快捷命令设置见 [工具说明](tools/README.md)。

### 本机日志名称目录

`CatalogEnabled` 默认关闭，开启后将实例及组件描述发布到固定的 `/tmp/mct-syslogger`，与 `TMPDIR`、状态维护及指标出口配置独立：

```go
manager, err := syslogger.New(syslogger.Config{
    Project: "contract-listener",
    InstanceID: "mainnet-a",
    InstanceDescription: "主网事件监听",
    CatalogEnabled: true,
})
if err != nil {
    return err
}
defer manager.Close(context.Background())
rpc, err := manager.Register(syslogger.ComponentConfig{
    Name: "rpc",
    Description: "区块与日志获取",
    QueueCapacity: 512,
})
if err != nil {
    return err
}
rpc.Info("RPC 已连接", nil)
```

构造成功后以本次空的业务组件集合覆盖旧清单，组件注册成功后发布完整新清单；状态维护开启时目录同时包含 `syslogger_manager`，容错初始化产生的结构化诊断也使用这个组件名。描述只供目录展示，日志及指标继续使用原有身份。

清单按项目和实例保存 JSON，项目目录名为项目原文的 SHA-256 十六进制摘要，实例文件名为实例 ID 的 SHA-256 十六进制摘要加 `.json`；名称与描述保存在 JSON 中，供工具以原文组织目录。固定长度路径兼容现有身份中的斜杠、空白及长项目名。

根目录和项目目录为 `0777`，JSON 文件为 `0666`，面向受信本机环境公开读写。创建目录时幂等复用，发布时使用同目录临时文件和原子替换。目录发布失败通过标准日志诊断，组件注册、日志及指标能力继续按原契约工作。

`Close` 保留清单，后续运行重新生成并覆盖，已移除组件随完整清单替换。同一项目和实例的并行发布以最后完成的写入为准。目录保存最近登记的名称，用于选择历史日志过滤条件；历史日志按 journal 留存范围查询，用户也可以直接输入目录中尚未登记的名称。共享临时目录可能被系统或管理员清理，后续运行及注册组件时重新发布。

## 健康与指标

`Healthy()`、`Degraded()`、`Unhealthy()` 无参数、无返回值，同步提交业务状态及库采集的时间。状态维护默认关闭，关闭时这些方法直接返回；日志与业务状态分别调用。四态为 0=Unknown、1=Healthy、2=Degraded、3=Unhealthy，Unknown 表示组件尚未形成业务或自动健康报告。较新业务入口时间整体替换业务记录，相同时间由后提交者覆盖，同状态也更新时间。

### 队满自动降级

```go
manager := syslogger.SafeNew(syslogger.Config{
    Project: "contract-listener",
    StateEnabled: true,
    DegradeOnQueueFull: true,
    QueueFullRecoveryInterval: 30 * time.Second,
})
```

`DegradeOnQueueFull` 默认关闭，启用时需要 `StateEnabled`。`QueueFullRecoveryInterval` 指定连续无队满丢弃的恢复等待时间，零值为 30 秒；`New` 拒绝负值，`SafeNew` 回退默认值并报告配置纠正。

各组件首次队满丢弃时产生自动 Degraded，再次队满丢弃重新计算恢复窗口；连续达到配置时长没有队满丢弃后，自动状态恢复 Healthy。恢复由最后一次队满时刻决定，窗口内成功接收日志或停止产生日志都可以自然到达恢复时刻；队列积压继续按原有发送流程交付。

业务主动上报的 Degraded 或 Unhealthy 持续优先，只有业务主动上报 Healthy 才解除该业务故障。有效状态同时包含仍生效的队满降级，因此窗口内调用 Healthy 后仍显示 Degraded，窗口结束后才显示 Healthy。编码失败、关闭后调用及后台发送失败继续使用各自的诊断和 Counter；自动恢复窗口由队满丢弃事件更新。

采集使用业务健康记录及最后队满时刻计算有效状态，HTTP 请求、周期文件快照和管理器内部采集使用同一规则。自动降级时间是最后队满时刻，自动恢复时间是该时刻加恢复窗口；最近的业务 Healthy 汇报继续保留其时间。首次关闭冻结当时的有效状态，最终文件快照及后续内部采集保持该截止结果。

### 指标出口

每个组件独立读写锁，采集复制单组件完整记录后释放锁，输出 I/O 在锁外执行。内置组件 `syslogger_manager` 表达管理器初始化、注册失败及实际注册恢复，具体来源走独立诊断。

| 指标 | 类型 | 含义 |
| --- | --- | --- |
| `syslogger_component_state` | Gauge | 当前四态数值 |
| `syslogger_component_timestamp_seconds` | Gauge | 当前有效健康记录的 Unix 整数秒，尚无健康报告时为 0 |
| `syslogger_component_log_rejected_total` | Counter | 模块入队前拒收的调用累计次数 |
| `syslogger_component_log_send_failures_total` | Counter | 模块后台发送失败的条目累计数 |

内置样本带上述三个身份标签。业务组件输出四条样本，内置管理器输出状态和时间。两个 Counter 从 0 累计，健康恢复不清零，管理器重建后归零；关闭期限到达时尚未尝试的积压通过 Close 错误反馈。

### 共享业务指标

`Manager` 实现官方 `prometheus.Collector`，直接读取现有健康记录和日志计数。`Config.Registry` 接收官方 `*prometheus.Registry`；省略时创建管理器独立的注册表，`manager.Registry()` 返回实际使用的注册表。默认表仅包含 syslogger 的 Collector，Go 和进程指标可由业务按需注册。

业务可在同一注册表中注册官方 GaugeVec、Counter、Histogram 等 Collector，指标保留自己的名称、标签及更新生命周期：

```go
registry := prometheus.NewRegistry()
manager, err := syslogger.New(syslogger.Config{
    Project: "contract-listener",
    StateEnabled: true,
    Registry: registry,
    HTTP: syslogger.HTTPConfig{Enabled: true},
})
if err != nil {
    return err
}
defer manager.Close(context.Background())
rpc, err := manager.Register(syslogger.ComponentConfig{Name: "rpc", QueueCapacity: 512})
if err != nil {
    return err
}
rpc.Healthy()

height := prometheus.NewGaugeVec(prometheus.GaugeOpts{
    Name: "alertdog_node_height",
    Help: "节点高度",
}, []string{"chain", "source"})
manager.Registry().MustRegister(height)
height.WithLabelValues("eth", "primary").Set(123)

handler, err := manager.Handler()
if err != nil {
    return err
}
mux.Handle("/metrics", handler)
```

该片段使用 `context`、`github.com/prometheus/client_golang/prometheus` 和已有的 `*http.ServeMux`。业务继续负责 RPC 探测及指标更新；syslogger 的 HTTP 与 `.prom` 文件出口通过同一个 Registry 采集并使用官方文本编码。两次采集之间数值可能变化，导出的指标集合及标签规则一致。

已有 `promhttp.HandlerFor(registry, promhttp.HandlerOpts{})` 可直接导出共享表，syslogger 只需开启 `StateEnabled` 维护健康记录。已有 `promhttp.Handler()` 使用 `prometheus.DefaultGatherer`，可将对应的同一个 `*prometheus.Registry` 传入 `Config.Registry`。`prometheus.DefaultRegisterer` 的类型是 `prometheus.Registerer` 接口，使用前需确认具体注册表及其与 Gatherer 的对应关系；显式创建并共享 Registry 更便于隔离和管理生命周期。

首次采集固定 syslogger 的组件集合，业务 Collector 仍可按官方规则动态注册、注销和更新。自定义 Collector 应支持并发采集，并在采集中读取及时可用的内存数据；HTTP 和文件交付会等待 Collector 完成。内置指标的项目和实例为常量标签，不同身份的管理器可以共用 Registry。同一身份或不兼容的指标描述符冲突由 `New` 返回官方注册错误；`SafeNew` 回退独立表后，接入方通过 `manager.Registry()` 取得实际表。

### HTTP 与文件交付

HTTP 与文件出口默认关闭，启用时须同时开启 StateEnabled。`StartHTTP()` 同步绑定后后台提供 `/metrics`。`StartFile()` 首次同步写入完整快照，成功后周期刷新，周期默认 10 秒。两个出口各自启动，失败可显式再次尝试，重复启动返回错误。

采集或编码失败时通过标准日志诊断，HTTP 返回 500，文件保留上一次完整快照；首次文件采集失败由 `StartFile` 返回错误，最终采集失败由 `Close` 返回错误。

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

首次 `Close(ctx)` 建立接收截止，并以该上下文限制日志排空；到期取消连接、释放积压记录并反馈未完成交付。只关闭库拥有的 HTTP 服务器，外部服务持有的 syslogger Handler 随后返回 503。已成功启动的文件出口串行写入最终快照并保留文件，未启动的出口保持原状态。最终快照之后注销自身 Collector，业务 Collector 和外部 promhttp 服务继续由调用方管理。重复 Close 复用收尾结果，后续 ctx 只限制本次等待。Collector 采集、文件系统和独立错误输出的底层 I/O 不承诺硬实时可取消。

```sh
go test ./...
go vet ./...
go test -race ./...
```

测试使用独立临时 socket 和目录，不写真实系统日志；环境中有 promtool 时自动检查实际编码的指标样本。真实 journald／rsyslog、日志中心、Prometheus／Alertmanager 联调和业务接入按对应环境授权另行验收。
