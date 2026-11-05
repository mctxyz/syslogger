# 专用 journal 配置

## 日志入口与存储

SDK 默认通过 `/run/systemd/journal.mct-syslogger/socket`，将结构化日志交给固定的 `mct-syslogger` journal 命名空间。该命名空间由独立的 journald 实例管理，拥有独立日志文件、容量及轮转策略。CLI 固定通过 `journalctl --namespace=mct-syslogger` 读取这些记录，项目、实例和组件筛选继续使用原生身份字段。

持久化数据通常位于 `/var/log/journal/<machine-id>.mct-syslogger/`；journald 暂用内存存储时位于 `/run/log/journal/` 下对应目录。日志保留范围由该实例的配置决定，各命名空间仍共享底层磁盘及其可用空间。

标准库 `log` 是独立故障诊断及无效组件句柄的兜底通道，保留宿主程序配置，默认写入 stderr；被 systemd 等宿主收集后仍可能出现在默认系统日志中。常规结构化日志通过专用入口发送，专用入口不可用时不会自动改投默认 journal 或系统 syslog。

## 主机准备

需要支持 journal namespaces 的 Linux 主机及 systemd 245+。以下操作由部署管理员执行；SDK 和 CLI 本身只使用已准备的入口，不修改主机配置。

创建 `/etc/systemd/journald@mct-syslogger.conf`：

```ini
[Journal]
Storage=persistent
SystemMaxUse=512M
RuntimeMaxUse=64M
ForwardToSyslog=no
ForwardToKMsg=no
ForwardToConsole=no
ForwardToWall=no
```

上述容量是部署示例，按业务日志量及主机空间调整。journald 按文件轮转和清理历史，容量设置不是逐字节的硬配额。四个转发选项用于将专用日志流保留在该实例内；若另有 `ForwardToSocket` 配置或外部采集器主动读取这个命名空间，也应核对其下游目标，避免重新汇入默认系统日志。

将专用 socket 加入开机启动依赖并立即启动：

```sh
sudo systemctl add-wants sockets.target systemd-journald@mct-syslogger.socket
sudo systemctl start systemd-journald@mct-syslogger.socket
```

`add-wants` 为实例 socket 建立持久的目标依赖，适用于模板没有 `[Install]` 段的发行版；socket 收到首条日志时按需激活对应 journald 实例。已有实例运行时，修改配置后重启该专用实例：

```sh
sudo systemctl restart systemd-journald@mct-syslogger.service
```

检查入口及存储：

```sh
systemctl status systemd-journald@mct-syslogger.socket
sudo journalctl --namespace=mct-syslogger --disk-usage
```

业务使用默认 `Config` 日志出口即可，保持 `SyslogSocketPath` 为空。SDK 直接连接专用 socket，业务服务的 stdout／stderr 仍由原宿主管理；无需给整个业务服务设置 `LogNamespace=`。若业务要求启动时入口已经就绪，可在其 systemd 服务的 `[Unit]` 中添加：

```ini
Requires=systemd-journald@mct-syslogger.socket
After=systemd-journald@mct-syslogger.socket
```

## 查看日志

```sh
# 专用命名空间的全部日志或单个项目。
sudo journalctl --namespace=mct-syslogger
sudo journalctl --namespace=mct-syslogger SYSLOGGER_PROJECT=contract-listener

# CLI 自动选用专用命名空间，继续支持实例、组件及实时跟踪。
mct-syslogger -p contract-listener -i mainnet-a -c rpc
mct-syslogger -p contract-listener -s now -f
```

普通 `journalctl` 显示默认命名空间；指定 `--namespace=mct-syslogger` 才读取这里的专用日志。读取权限沿用系统 journal 的组和 ACL 配置，命名空间用于日志流及留存隔离，不是管理员权限隔离。

## 升级与排障

- 先准备专用命名空间，再配套升级业务使用的 SDK 和 CLI。原先默认 journal 中的历史记录按原留存策略存在，可用 `journalctl SYSLOGGER_PROJECT=项目名` 或旧记录的项目 tag 查询；新 CLI 固定读取专用命名空间，历史数据不会自动迁移。
- SDK 继续在实际发送时连接日志服务。`New`／`SafeNew` 成功表示管理器创建完成，不表示 journald 已经可用。连接失败沿用原生 `log` 故障诊断、发送失败计数、失败条目放弃及后续日志重连行为；同步方法直接返回发送错误。
- `SyslogSocketPath` 是传统 syslog 协议选项，适用于显式部署的独立 syslog 服务和隔离测试。原生 journal socket 不能填入该选项；指定自定义路径时，由接入方保证其留存及转发目标独立。
- 非 Linux 平台的默认发送返回不支持错误，显式传统 syslog 入口继续可用。名称目录和指标出口维持各自的平台及配置契约。

官方参考：[journal namespaces](https://www.freedesktop.org/software/systemd/man/latest/systemd-journald.service.html#Journal%20Namespaces)、[journald.conf](https://www.freedesktop.org/software/systemd/man/latest/journald.conf.html)、[journalctl](https://www.freedesktop.org/software/systemd/man/latest/journalctl.html)。
