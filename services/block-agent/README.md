# Block Agent、PLC Simulator 与可选 BDM 上行

`block-agent` 是一台 Block 对应一台设备的本地运行核心。没有 Wi-Fi、路由器
或 BDM 时，采集、状态、本地 HMI、报警、历史、审计和允许的现场操作仍须
正常运行。实验开发闭环全部走 Unix socket：

```text
plc-simulator -- /run/block-plc/io/io.sock -- block-agent
block-agent   -- /run/block-agent/api/block-agent.sock -- block-hmi
```

启用 `bdm.enabled=true` 后，Agent 另起后台上行分支，主动通过 MQTTS 把本地
数据副本发送给 BDM。BDM 只是可选只读数据平台；证书错误、网络中断、Broker
不可达或应用确认延迟都不会终止本地采样和 HMI。Agent、Simulator 都不监听
业务 TCP 端口，也不读取 Wi-Fi 配置。

## 组件

- `cmd/plc-simulator`：实验用语义级设备模型，提供确定性生产节拍、计数、
  料仓、报警、安全联锁、命令执行、故障注入和重启持久化；不模拟厂商寄存器
  或现场总线。
- `cmd/block-agent`：采样、数据新鲜度、单写命令队列、`commandId` 幂等、
  SQLite WAL、本地报警/历史/审计、HMI 兼容适配层和可选 BDM 上行。
- `cmd/ssh-bootstrapd`：独立 HTTPS `9443/tcp` 管理服务，严格实现 Common
  `contracts/ssh-bootstrap/v1` 的 SuperToken ED25519 验签、SQLite nonce
  唯一登记和五分钟 OpenSSH 用户证书签发；不进入 Block 本地业务依赖。
- `internal/bdm`、`internal/mqtt5`：MQTT 5 持久会话、重连、MQTTS/mTLS、
  上行发布和唯一允许的 `/down/sync` 处理。
- `internal/storage`、`internal/uplink`：稳定 `messageId`、可靠流 Epoch/序号、
  SQLite Outbox、Gap 账本、Replay 和应用层确认事务。
- `internal/plccontract`：仅在 Block 仓库内部使用的私有语义协议
  `block-plc-private/v1`，不是 Common 公共契约。

## 构建与测试

所有缓存和输出应放在 `D:\codex\Block-DMP\.cache\**`。SQLite 驱动是纯 Go
实现，Linux ARM64 发布保持 `CGO_ENABLED=0`。

```powershell
$env:TEMP = 'D:\codex\Block-DMP\.cache\block-agent\tmp'
$env:TMP = $env:TEMP
$env:TMPDIR = $env:TEMP
$env:GOTMPDIR = 'D:\codex\Block-DMP\.cache\block-agent\gotmp'
$env:GOCACHE = 'D:\codex\Block-DMP\.cache\block-agent\gocache'
$env:GOMODCACHE = 'D:\codex\Block-DMP\.cache\block-agent\gomodcache'

go test ./...
go vet ./...
go test -race ./...

$env:CGO_ENABLED = '0'
$env:GOOS = 'linux'
$env:GOARCH = 'arm64'
go build -trimpath -o `
  'D:\codex\Block-DMP\.cache\block-agent\bin\block-agent' ./cmd/block-agent
go build -trimpath -o `
  'D:\codex\Block-DMP\.cache\block-agent\bin\plc-simulator' ./cmd/plc-simulator
go build -trimpath -o `
  'D:\codex\Block-DMP\.cache\block-agent\bin\ssh-bootstrapd' ./cmd/ssh-bootstrapd
```

## 配置与启动

所有配置文件路径必须是绝对路径；未知 JSON 字段会使进程启动失败。

```bash
./plc-simulator -config /etc/block/plc-simulator.json
./block-agent -config /etc/block/block-agent.json
```

- 真实设备适配器尚未接入；非模拟部署使用 `adapter.type: "disabled"`。
- 只有实验环境使用
  `deploy/block/config/block-agent-simulator.example.json` 并启动 Simulator。
- `bdm.enabled=false` 是独立运行默认值：不新建 uplink Epoch、不生成 Outbox、
  不拨号；已经存在的 Epoch、Outbox 和确认水位不会被删除。
- 启用 BDM 时，由 Release 从
  `deploy/block/config/block-agent-simulator-bdm.example.json` 或
  `block-agent-bdm.example.json` 生成现场配置，并注入端点、opaque principal、
  服务端 CA、Block 客户端证书/私钥、软件/OS/架构/硬件版本事实和
  `streamGeneration`。样例中的 `replace-at-release` 不能直接部署。
- 当前实验路由是 `mqtts://192.168.1.105:8883`；该地址只用于路由和证书
  IP SAN 校验，不是业务身份。业务身份始终是配置中的
  `siteId`、`blockId`、`deviceId`。

启用配置只接受 `mqtts://HOST:8883`，不接受 URL 凭据、路径、查询或明文
`mqtt://...:1883`。客户端固定 TLS 1.2/1.3，校验 BDM CA、完整服务端链、
有效期和主机名/IP SAN；客户端证书必须有效、具有 `clientAuth` EKU，且
CN 精确等于 `blk-<32 位小写十六进制>` principal。代码没有证书校验绕过或
明文降级路径。

## Unix socket 本地 API

Simulator I/O socket：

- `GET /healthz`
- `GET /v1/snapshot`
- `POST /v1/commands`

Simulator 控制 socket（仅实验故障注入）：

- `POST /v1/faults`
- `POST /v1/restart`

可注入断开、响应延迟、冻结数据、`GOOD`/`UNCERTAIN`/`BAD` 质量、急停、
门禁打开、严重报警、命令拒绝、命令失败以及“已执行但响应超时”。控制
socket 不暴露给 HMI。

Agent 本地 API socket：

- `GET /healthz`
- `GET /api/v1/state`
- `PUT /api/v1/settings`
- `POST /api/v1/commands`
- `POST /api/v1/alarms/{id}/ack`
- `GET /api/v1/audit`

这些写操作只属于 Block 现场本地接口，不会从 BDM 或 Pad 调用。请求必须带
稳定 `Idempotency-Key`；Agent 在发送前写入 SQLite，按单队列执行，并在
独立读回确认后才标记 `EXECUTED`。传输结果不确定时记录 `UNKNOWN`，不会
盲目重发。报警确认只改变本地确认状态，不绕过急停、门禁或安全联锁。

## MQTTS v1 可靠上行

实现以 Common `contracts/mqtt/v1` 为准：

- 上行仅有 Presence、Hello、Heartbeat、Snapshot、Event、Alarm、Replay
  和 Sync Status；Telemetry 当前未启用。
- 唯一 BDM→Block Topic 是精确的非 Retain QoS 1 `/down/sync`。配置、任务、
  命令、更新和告警确认均不接受。
- 证书 CN、MQTT Client ID 和 username 使用同一个 opaque principal；
  Topic 和消息 `source` 使用稳定业务 ID，不能由 IP 或主机名推导。
- Snapshot、Event、Alarm 在保存本地状态的同一个 SQLite 事务中获得稳定
  `messageId`、持久 Epoch 和严格递增序号并进入 Outbox。
- MQTT PUBACK 只确认 Broker 收包，不删除 Outbox。只有 BDM 数据库事务
  提交后发布的 `/down/sync` 连续应用确认，才能在一个 SQLite 事务中推进
  水位、处理 Gap 并删除已确认记录。
- 断线期间继续积压可靠 Outbox；MQTT 会话和 in-flight QoS 1 状态也持久化。
  恢复后先完成 Presence/Hello/Sync Status，再按序直发或 Replay，且不会
  因迟到确认降低水位。

## 持久化、失联和当前限制

- SQLite 使用 WAL、`synchronous=FULL` 和单连接串行写入。
- 保存最新快照、当前报警、报警历史、操作历史、命令结果、审计、可靠
  Outbox、Gap 账本、同步水位和 MQTT in-flight 状态。
- Simulator 保存计数器、设置和已处理命令 ID；重启生成新会话 ID，序号在
  会话内单调递增。
- Simulator 断开后 Agent 继续存活并保留最后快照；超过 `staleAfter` 后
  状态读取返回 `503 backend_unavailable`，HMI 禁止新的现场写操作。
- Agent 重启恢复 SQLite 快照和幂等记录；崩溃前仍为 `PENDING` 的命令恢复
  为 `UNKNOWN`，不会盲目重发。
- 当前没有真实 PLC/现场总线适配器，不实现 BDM/Pad 远程控制、远程配置、
  远程升级、MES、AGV 或企业服务器。

部署、健康检查、版本记录和回滚见 `deploy/block/README.md`。
SSH 短期证书管理入口的独立配置、systemd、sshd drop-in、静态验证和回滚见
`deploy/block/ssh-bootstrap/README.md`。
