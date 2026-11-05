# 日志发送与检索

SDK 按组件发送结构化日志，身份来自[管理器和组件注册](sdk.md#身份与注册)。Linux 默认使用专用 journal，主机准备见[部署说明](journal.md)。

## 异步日志与事件格式

普通异步方法为 `Emergency、Alert、Critical、Error、Warning、Notice、Info、Debug`，对应原生 severity 0～7。方法接收 `(message string, fields Fields)`，无返回值，字段可传 nil。`Fields` 是以 `map[string]any` 为底层类型的结构化业务字段类型，调用方使用 `syslogger.Fields` 构造字段；八级事件统一提交，等级过滤交由设备日志服务配置，进程退出由业务管理。

```go
rpc.Info("RPC 已连接", syslogger.Fields{
    "endpoint": "primary",
})
rpc.Healthy()
```

日志与业务健康状态分别调用，健康维护的启用方式见[健康与指标](metrics.md)。日志正文示例：

```json
{"timestamp":"2026-10-22T08:00:00Z","level":"error","project":"contract-listener","instance_id":"common","component":"rpc","message":"RPC 请求超时","fields":{"error":"context deadline exceeded","duration_ms":3000}}
```

正文实际为单行 JSON，时间为 UTC RFC3339Nano，等级为小写名称。顶层 error 字段使用 Error() 文本，nil 或空字段省略 fields。编码在调用线程完成，调用期间字段由业务保证无并发修改；返回后修改原字段不会改变已编码日志。出口使用 LOG_USER facility 和真实 severity，`SYSLOG_IDENTIFIER`／syslog tag 来自项目名；项目 tag 中的空白和控制字符规范化为下划线，JSON 身份保留原文。

## 日志入口与协议

Linux 默认通过 `/run/systemd/journal.mct-syslogger/socket` 的原生 journal 协议写入固定的 `mct-syslogger` 命名空间，同时发送 `SYSLOGGER_PROJECT`、`SYSLOGGER_INSTANCE_ID`、`SYSLOGGER_COMPONENT` 三个独立原文身份字段，供 journal 索引检索；`MESSAGE` 保持上述完整 JSON 字节。身份中的换行使用原生二进制字段编码，大消息在数据报超限时通过密封的匿名内存文件传递。SDK 拥有连接，连接和写入各使用一秒等待上限；普通异步调用线程只编码和入队。

专用命名空间由管理员在 systemd 245+ 主机上预先启用，独立管理存储和轮转，部署步骤见[专用 journal 配置](journal.md)。默认入口不可连接时按发送失败处理，不自动转写默认 journal 或系统 syslog，原生 `log` 故障诊断继续保留。其他平台的默认发送返回不支持错误，可通过显式 `SyslogSocketPath` 接入独立传统 syslog 服务；自定义入口的隔离和转发策略由部署方保证。

`SyslogSocketPath` 可指定部署或隔离测试的传统 syslog 路径，按 Unix datagram、stream 顺序连接该路径；原生 journal socket 使用默认配置接入，不能填入这个传统协议选项。默认异步连接延迟到后台首次发送，同一组件复用成功连接，发送失败后在后续事件重新连接同一专用入口。设备负责实际留存、过滤、限流及中心转发，提交 Debug 不保证最终可查询。标准库 `log` 仍承担独立故障诊断和无效组件句柄兜底，默认写入 stderr，被宿主收集后可能进入默认系统日志。

## 队列与发送失败

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

普通队列满时立即拒收新日志，更新拒收 Counter 并通过标准日志诊断，已接收的队列记录保持 FIFO。发送成功只表示本机 socket 写入成功，中心接收和持久化由设备日志链路管理。

普通异步调用的编码失败、队满或管理器关闭后拒收，通过标准库 `log.Printf` 输出独立诊断，并累计模块拒收次数；提交诊断按模块每秒最多输出一次，Counter 保留全部次数。后台连接或写入失败沿用独立标准日志诊断和发送失败 Counter，失败条目即放弃。连接恢复服务后续日志，不重放失败记录。本机连接及写入等待各受有限超时约束，业务前台仍承担编码和必要的标准日志输出成本，库不承诺硬实时调用上限。

普通异步方法在空组件句柄及缺少管理器时通过标准日志输出原有句柄警告，下一行输出采用相同事件格式的单行 JSON，保留等级、时间、消息及业务字段。此时项目和实例身份使用空字符串，组件名称存在时保留；警告与事件使用同一次 `log.Printf` 输出。业务字段编码失败时保留句柄警告，并在下一行输出编码失败原因。

## 同步日志

八个等级分别提供 `EmergencySync、AlertSync、CriticalSync、ErrorSync、WarningSync、NoticeSync、InfoSync、DebugSync`，签名统一为 `(ctx context.Context, message string, fields Fields) error`。同步方法使用相同身份、JSON 编码和 journal／syslog 协议，每次建立独立短连接，在当前调用中完成发送尝试并关闭连接，适用于退出前的关键事件。

同步调用直接发送，普通队列容量不影响其接收；它可以先于积压记录发送，并发同步调用之间也不承诺顺序。`ctx` 必须非 nil，可取消连接和写入；未设置更短期限时，连接和写入仍各受一秒上限约束。JSON 编码及匿名内存文件的准备不承诺硬实时取消。返回 nil 仅表示写入本机 socket，不代表 journald 持久化或中心接收。

编码、无效上下文、预先取消或关闭拒收累计接收前拒收次数；通过接收检查后的连接、写入或取消失败累计发送失败次数。同步调用直接返回错误，由业务处理诊断和退出。关闭后的调用返回 `ErrClosed`；已接收调用纳入 `Close` 等待，关闭期限到达时中断同步发送。普通异步方法和业务健康状态保持原有行为。

```go
ctx, cancel := context.WithTimeout(context.Background(), time.Second)
err := rpc.ErrorSync(ctx, "无法继续运行", syslogger.Fields{"error": cause})
cancel()
if err != nil {
    fmt.Fprintf(os.Stderr, "无法继续运行；日志发送失败：%v\n", err)
}
os.Exit(1)
```

退出由业务显式调用；`os.Exit` 不执行 defer，需要完整收尾时，应在退出前使用新的有效上下文调用 `manager.Close`。同步调用只交付本条日志，不负责排空普通队列或立即保存目录活动。

## 日志检索

[mct-syslogger](../tools/mct-syslogger) 提供独立 Go 命令，支持项目／实例／组件目录、日志过滤、实时跟踪、彩色输出和 JSON 输出。在仓库根目录编译运行：

```sh
go build -o tools/bin/mct-syslogger ./tools/mct-syslogger
./tools/bin/mct-syslogger --list
./tools/bin/mct-syslogger -p contract-listener -i local-a -c rpc -s=-2h -k '超时'
```

CLI 固定通过 `journalctl --namespace=mct-syslogger` 查询专用命名空间，将项目、实例、组件条件直接交给 `SYSLOGGER_PROJECT`、`SYSLOGGER_INSTANCE_ID`、`SYSLOGGER_COMPONENT` 原生字段索引，随后继续复核 JSON 正文。应用与 CLI 需配套升级，旧默认 journal 中的历史记录继续用普通 `journalctl` 查询，迁移说明见[专用 journal 配置](journal.md#升级与排障)。身份筛选依赖对应原生字段，传统 syslog 出口的记录不具备这些索引；外部采集转发时也应保留三个身份字段。

编译使用 Go 1.23+，日志查询运行环境为 Linux、systemd 245+ 和 journalctl。Release 下载、安装与完整参数见[工具说明](../tools/README.md)。
