# syslogger

Go 1.23 本机结构化日志与组件健康状态库，配套 `mct-syslogger` 命令工具。日志按等级写入四个专用 journal 空间，指标使用 Prometheus 官方客户端。

```text
日志 → 每组件四组独立队列与并行发送 → debug / info（含 notice）/ warning / error 及以上四个 journal 空间
状态 → 内存记录 → HTTP／.prom → Prometheus
```

## 命令工具安装与初始化

安装脚本支持 Linux amd64／arm64，需要交互终端及 root 或 sudo：

```bash
bash -o pipefail -c 'curl -fsSL https://raw.githubusercontent.com/mctxyz/syslogger/main/tools/install/install.sh | bash'
```

脚本下载并校验最新 Release，检查参数接口后，询问**安装前缀**（默认 `/usr/local`），确认后安装或更新程序及配套文档。首次安装默认也只安装程序，保留主机日志配置和 journald 服务状态；命令默认安装到 `/usr/local/bin/mct-syslogger`。

需要一并初始化日志接收入口时，显式使用 `--init`：

```bash
bash -o pipefail -c 'curl -fsSL https://raw.githubusercontent.com/mctxyz/syslogger/main/tools/install/install.sh | bash -s -- --init'
```

该模式另需 systemd 245+、`groupadd` 及初始化系统工具，并询问四个 journal 空间的持久化容量，依次默认 `1G/2G/1G/1G`。`--init` 会重新生成受管 journal 配置，覆盖其中的手动修改，并重启四个专用 journald 实例；管理员独立配置保留并参与冲突检查。

初始化准备 `mct-syslogger` 共享组，创建或修正 `root:mct-syslogger`、`2775` 的名称根目录及已有受管项目目录，并重启四个专用 journald 实例。各程序可以使用独立账户，由管理员将其加入共享组并重启程序刷新组身份；SDK 新项目目录为 `2775`，初始保留创建者 UID，后续 init 可统一为 root。清单及锁为 `0664`，保留原 UID 和内容，通过属组协作维护。

权限迁移前先停止相关写入进程，再运行配套 init 自动修正受管对象，保留清单内容及稳定锁 inode。无关项保持原样；受管链接、特殊类型、硬链接或 ACL 等异常需管理员处理后重试，见[名称目录权限](docs/catalog.md#权限与故障处理)。缩小容量可能清理对应空间的旧归档，失败时已完成步骤保留。旧单空间的手动清理见[部署说明](docs/journal.md#升级与手动清理旧空间)。

源码中可运行 `bash tools/install/install.sh` 安装或更新，使用 `bash tools/install/install.sh --init` 安装并初始化，帮助使用 `bash tools/install/install.sh --help`。程序安装后也可单独执行 `sudo mct-syslogger init`。依赖、手工下载和完整安装说明见 [工具文档](tools/README.md#release-下载与安装)。

## 接入 SDK

导入 `context` 和 `github.com/mctxyz/syslogger`。以下片段放在返回 `error` 的业务函数中，管理器的生命周期应覆盖业务使用期；示例显式开启名称目录，需要先完成主机初始化。

```go
manager, err := syslogger.New(syslogger.Config{
    Project: "myapp",
    InstanceID: "main",
    CatalogEnabled: true,
})
if err != nil {
    return err
}
defer manager.Close(context.Background())

rpc, err := manager.Register(syslogger.ComponentConfig{
    Name: "rpc",
    Description: "区块与日志获取",
})
if err != nil {
    return err
}
rpc.Info("RPC 已连接", syslogger.Fields{"endpoint": "primary"})
```

`New` 严格校验配置；需要纠正配置后继续运行时使用 `SafeNew`。完整配置、注册顺序和关闭语义见 [SDK 接入](docs/sdk.md)。

## 查询日志

先查看目录，再用实际的项目、实例和组件名称查询。下方身份与 SDK 示例对应：

```sh
mct-syslogger --list
mct-syslogger -p myapp -i main -c rpc -s=-2h
mct-syslogger -p myapp -i main -s now -f
```

`--json` 适合脚本，`--help` 查看完整参数。筛选、输出及排障示例见 [命令工具](tools/README.md)。

## 使用约定

- **身份**：日志和指标共用 `project、instance_id、component`；空实例 ID 使用 `common`，同项目的并行实例应使用不同且跨重启稳定的 ID。
- **日志交付**：默认异步入队，每组件四组各默认 512 条，独立容量、发送任务和连接，组内 FIFO、组间并发。容量配置按每组解释；关键事件可用八级 `*Sync(ctx, message, fields)` 方法。写入成功仅表示交付本机 socket；默认专用入口失败不会自动转写系统 journal，失败条目不重放。
- **健康与指标**：状态维护、HTTP 和文件出口默认关闭。开启健康维护使用 `StateEnabled`；组件健康样本还需 `EnableHealthMetrics: true`。业务按组件集中声明 `HealthName` 类型的名称常量，通过无返回值的 `Healthy(name)`、`Degraded(name)`、`Unhealthy(name)` 上报，非法名称拒绝写入并由库独立诊断；健康指标使用 `name` 标签，SDK 自动生成 `name="overall"` 的组件整体。全部健康时整体 Healthy，全部不健康时整体 Unhealthy，其余组合 Degraded。关闭健康导出不影响按组件维护的两个日志 Counter，普通日志不自动改变业务健康结论。类型转换与接入示例见[健康与指标](docs/metrics.md)。
- **名称目录**：`CatalogEnabled` 默认关闭，开启后按实际接收日志建立和更新目录；目录记录不代表进程存活或 journal 当前留存。

## 文档导航

| 主题 | 内容 |
| --- | --- |
| [SDK 接入](docs/sdk.md) | 身份、注册、SafeNew 配置纠正、Close 生命周期 |
| [日志发送](docs/logging.md) | 日志格式、协议、队列、失败处理、同步日志 |
| [名称目录](docs/catalog.md) | 清单结构、权限、每小时接收量、保留和清理 |
| [健康与指标](docs/metrics.md) | 状态、队满降级、共享 Registry、HTTP 与文件出口 |
| [主机部署](docs/journal.md) | init、专用 journal、容量、权限和手动清理 |
| [命令工具](tools/README.md) | 安装、名称发现、查询参数、性能诊断、CI 发布 |

## 开发验证

CI 固定 Go 1.23.12；本地可使用同一工具链：

```sh
GOTOOLCHAIN=go1.23.12 go test -race ./...
GOTOOLCHAIN=go1.23.12 go vet ./...
```

测试使用隔离目录和 socket；安装脚本测试需要 util-linux 的 `script`，不执行真实系统安装或主机初始化。
