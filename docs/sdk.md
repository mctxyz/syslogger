# SDK 接入与生命周期

syslogger 的 module 为 `github.com/mctxyz/syslogger`，包名为 `syslogger`。日志、健康指标和名称目录共用项目、实例、组件身份。

## 身份与注册

```go
import "github.com/mctxyz/syslogger"
```

日志和内置指标共用 `project、instance_id、component`，健康指标另以 `name` 区分组件内自定义状态及自动生成的 overall。业务代码固定项目名和组件名，二者保留非空白且合法 UTF-8 的原文；业务入口从配置文件等来源读取实例 ID 后传给库，空实例 ID 使用 `common`，显式非空值按 3～128 字节和 UTF-8 校验并保留原文。

下方为初始化片段，业务在 `New` 成功后接管管理器的关闭职责，后续初始化失败也需要收尾；正常运行时，管理器的生命周期应覆盖业务使用期：

```go
const rpcConnection syslogger.HealthName = "connection"

manager, err := syslogger.New(syslogger.Config{
    Project: "contract-listener",
    InstanceID: instanceID,
    StateEnabled: true,
    HTTP: syslogger.HTTPConfig{Enabled: true, ListenAddr: ":9091"},
})
if err != nil {
    return err
}
defer manager.Close(context.Background())
rpc, err := manager.Register(syslogger.ComponentConfig{
    Name: "rpc",
    Description: "区块与日志获取",
    EnableHealthMetrics: true,
    QueueCapacity: 512,
})
if err != nil {
    return err
}
if err := manager.StartHTTP(); err != nil {
    return err
}
rpc.Unhealthy(rpcConnection)
```

已有服务器通过 `Handler()` 挂载，服务器归业务管理。模块在取得 Handler、开始启动指标出口或首次通过 Registry 采集之前完成注册；配置及组件集合之后固定。模块注册后日志即可使用，业务退出时调用 `Close(ctx)`。

同一项目的并行实例应显式提供不同 ID，跨重启复用。多个实例使用 common 会共享逻辑身份；各实例的指标文件路径由接入方分别配置。

每组件拥有 debug、info/notice、warning、error 及以上四组独立队列、发送任务和连接，组间并发，组内 FIFO。`DefaultQueueCapacity` 和组件 `QueueCapacity` 的单位均为每组容量，默认各 512 条，每组件共 2048 个缓冲槽位；容量范围、覆盖及迁移说明见[日志队列](logging.md#队列与发送失败)。Linux 默认发送到对应四个专用 journal 空间；主机的独立留存容量由 `mct-syslogger init` 管理，见[部署说明](journal.md)。

## 容错初始化

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
| `DefaultQueueCapacity` 为零 | 正常使用每组默认值 512 |
| `DefaultQueueCapacity` 为负或大于 4096 | 回退每组 512，记录 Warning |
| 指标文件已启用，但 `OutputPath` 为空或全空白 | 停用指标文件出口，记录 Error；HTTP 和 syslog 配置保持原样 |
| 指标文件已启用，刷新间隔为负 | 回退 10 秒，记录 Warning |
| 指标文件已启用，刷新间隔为零 | 正常使用默认值 10 秒 |
| 队满自动降级已启用，`QueueFullRecoveryInterval` 为负 | 回退 30 秒，记录 Warning |
| 队满自动降级已启用，`QueueFullRecoveryInterval` 为零 | 正常使用默认值 30 秒 |
| 纠正后仍有指标出口或队满自动降级启用，但状态维护关闭 | 自动开启 `StateEnabled`，记录 Warning |

合法身份保留原文，多个非法身份可能共享同一回退身份。已关闭的指标文件配置沿用原有处理语义；状态依赖根据纠正后仍启用的指标出口和队满自动降级功能补齐。

每项异常首先由标准库 `log.Printf` 输出，默认写入 stderr，接入方可通过标准日志配置调整目标。库同时通过独立的初始化诊断日志管线，按相同四组路由尝试以 `component="syslogger_manager"` 交付结构化 Warning／Error syslog 日志，业务字段包含 `state="unhealthy"`、字段名、原因和纠正结果。该管线由管理器关闭，补充诊断交付失败通过标准日志反馈。

开启状态维护时，发生配置纠正的管理器持续显示 `Unhealthy`（3），后续组件注册成功保持该配置异常事实。正常采用默认值保持正常初始化状态；HTTP、指标文件仍按原有方式显式接入或启动，系统日志留存及外部资源可用性由对应环境决定。

## 关闭

首次 `Close(ctx)` 建立统一接收截止，四组并行排空，并以该上下文限制全部组及已接收的同步发送；到期取消各组及同步连接、释放积压记录并反馈未完成交付。只关闭库拥有的 HTTP 服务器，外部服务持有的 syslogger Handler 随后返回 503。已成功启动的文件出口串行写入最终快照并保留文件，未启动的出口保持原状态。最终快照之后注销自身 Collector，业务 Collector 和外部 promhttp 服务继续由调用方管理。重复 Close 复用收尾结果，后续 ctx 只限制本次等待。Collector 采集、文件系统和独立错误输出的底层 I/O 不承诺硬实时可取消。

退出由业务显式调用；`os.Exit` 不执行 defer，需要完整收尾时，应在退出前使用新的有效上下文调用 `manager.Close`。同步日志只交付本条记录，退出前关键事件的用法见[同步日志](logging.md#同步日志)。

## 验证范围

测试使用独立临时 socket 和目录，不写真实系统日志；环境中有 promtool 时自动检查实际编码的指标样本。真实 journald／rsyslog、日志中心、Prometheus／Alertmanager 联调和业务接入按对应环境授权另行验收。
