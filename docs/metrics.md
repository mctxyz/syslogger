# 健康与指标

组件健康状态由业务主动报告，日志发送和健康报告分别调用。HTTP、指标文件和外部 Registry 复用 Prometheus 官方 Collector。

## 状态维护与逐组件导出

`Healthy()`、`Degraded()`、`Unhealthy()` 无参数、无返回值，同步提交业务状态及库采集的时间。状态维护默认关闭，关闭时这些方法直接返回；日志与业务状态分别调用。四态为 0=Unknown、1=Healthy、2=Degraded、3=Unhealthy，Unknown 表示组件尚未形成业务或自动健康报告。较新业务入口时间整体替换业务记录，相同时间由后提交者覆盖，同状态也更新时间。

`ComponentConfig.EnableHealthMetrics` 控制该组件是否导出 `syslogger_component_state` 和 `syslogger_component_timestamp_seconds`，默认 `false` 不输出这两项样本，需要时在注册阶段显式开启：

```go
rpc, err := manager.Register(syslogger.ComponentConfig{
    Name: "rpc",
    EnableHealthMetrics: true,
})
if err != nil {
    return err
}
runtime, err := manager.Register(syslogger.ComponentConfig{Name: "runtime"})
if err != nil {
    return err
}
rpc.Healthy()
runtime.Info("进程已启动", nil)
```

该选项只控制导出，注册后固定：全局 `Config.StateEnabled` 仍控制健康记录维护，`DegradeOnQueueFull` 仍按原规则更新有效状态；显式开启导出但状态维护关闭时，健康样本保持初始 Unknown/0。两个日志 Counter 始终按原日志路径维护并参与采集，日志发送、同步日志及名称目录小时接收量不受此选项影响。管理器内置 `syslogger_manager` 的健康指标继续遵守原有全局状态配置。

HTTP、文件和外部共享 Registry 使用同一个 Collector，按组件采用相同的导出选择。升级前已依赖组件健康指标的调用方，需要为对应注册补充 `EnableHealthMetrics: true`；未启用的组件没有这两项样本，而不是输出零值。

## 队满自动降级

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

## 指标定义

每个组件独立读写锁，采集复制单组件完整记录后释放锁，输出 I/O 在锁外执行。内置组件 `syslogger_manager` 表达管理器初始化、注册失败及实际注册恢复，具体来源走独立诊断。

| 指标 | 类型 | 含义 |
| --- | --- | --- |
| `syslogger_component_state` | Gauge | 当前四态数值 |
| `syslogger_component_timestamp_seconds` | Gauge | 当前有效健康记录的 Unix 整数秒，尚无健康报告时为 0 |
| `syslogger_component_log_rejected_total` | Counter | 模块接收前拒收的调用累计次数 |
| `syslogger_component_log_send_failures_total` | Counter | 模块异步及同步发送失败的条目累计数 |

内置样本带 `project、instance_id、component` 三个身份标签。业务组件始终输出两个日志 Counter，显式开启 `EnableHealthMetrics` 后再输出状态和状态时间，内置管理器输出状态和时间。两个 Counter 从 0 累计，健康恢复不清零，管理器重建后归零；关闭期限到达时尚未尝试的积压通过 Close 错误反馈。

官方文本编码为状态指标族输出一条共享的 HELP 注释，所有组件的状态样本共用数值说明；HTTP 与 `.prom` 文件均包含该注释：

```text
# HELP syslogger_component_state 组件健康状态：0=Unknown（尚无健康报告），1=Healthy（正常），2=Degraded（部分能力受损），3=Unhealthy（无法正常履行职责）。
# TYPE syslogger_component_state gauge
```

## 共享业务指标

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
rpc, err := manager.Register(syslogger.ComponentConfig{Name: "rpc", QueueCapacity: 512, EnableHealthMetrics: true})
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

## HTTP 与文件交付

HTTP 与文件出口默认关闭，启用时须同时开启 `StateEnabled`。`StartHTTP()` 同步绑定后后台提供 `/metrics`。`StartFile()` 首次同步写入完整快照，成功后周期刷新，周期默认 10 秒。两个出口各自启动，失败可显式再次尝试，重复启动返回错误。

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
