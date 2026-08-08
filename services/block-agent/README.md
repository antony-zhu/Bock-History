# Block Agent、PLC Simulator 与可选 MQTTS v2 只读同步

`block-agent` 是一台 Block 对应一台设备的本地运行核心。没有 Wi-Fi、路由器
或 BDM 时，采集、状态、本地 HMI、报警、历史、审计和允许的现场操作仍须
正常运行。PLC 模拟器与 Agent 的实验 I/O 闭环走 Unix socket：

```text
plc-simulator -- /run/block-plc/io/io.sock -- block-agent
```

启用 `BLOCK_MQTTS_V2_ENABLED=true` 后，Agent 通过 MQTTS v2 向 BDM 提供当前
状态和报警历史只读同步。BDM 只是可选数据平台；证书错误、网络中断或 Broker
不可达都不会终止本地采样和 HMI。Simulator 不监听业务 TCP；Agent 的 HMI 业务
仅监听 loopback TLS，且不读取 Wi-Fi 配置。

## 本机 HMI TLS 边界

`block-agent` 的嵌入式 HMI、`/healthz`、`/api/*` 和 `/ws` 只在
`127.0.0.1:8444` 的 HTTPS/WSS listener 上提供。`/ws` 不接受 `ws://` 降级；
前端必须使用同源 `wss://127.0.0.1:8444/ws`。8080、8081 与任何明文 HTTP 兼容
监听均不存在，也不做重定向。

维护 HTTPS 仍独立运行在 `0.0.0.0:8443`，只承担既有维护页面与 SSH 密钥下载；
它不复用为 HMI 业务 mux。启动时必须提供本机 TLS 证书和私钥，缺失或无法加载
即为致命错误。TLS 最低版本为 1.2，客户端必须校验证书链和 `127.0.0.1` 主机名；
代码没有 `InsecureSkipVerify` 或明文降级。

systemd 使用以下 flags（路径来自受保护的 `/etc/block/block.env`）：

```text
-local-https-address 127.0.0.1:8444
-local-tls-cert /etc/block/certs/block-hmi.crt
-local-tls-key /etc/block/certs/block-hmi.key
```

本机 HMI 的严格校验 CA 是公开、非私钥的
`/usr/local/share/ca-certificates/block-dmp-blk-rel-001.crt`。它必须可由
`block` 与 `block-ui` 读取；kiosk 不使用不可读的 `/etc/block/certs/ca.crt`。

本机 HMI 使用单独的 leaf/私钥；公开 CA 另供 kiosk 和健康检查使用。证书必须带有
`127.0.0.1` 的 SAN。私钥、真实环境文件及证书内容均不得提交。

## 组件

- `cmd/plc-simulator`：实验用语义级设备模型，提供确定性生产节拍、计数、
  料仓、报警、安全联锁、命令执行、故障注入和重启持久化；不模拟厂商寄存器
  或现场总线。
- `cmd/block-agent`：采样、数据新鲜度、单写命令队列、`commandId` 幂等、
  SQLite WAL、本地报警/历史/审计、HMI 兼容适配层和可选 MQTTS v2 当前状态、
  报警历史只读同步。
- `cmd/ssh-bootstrapd`：独立 HTTPS `9443/tcp` 管理服务，严格实现 Common
  `contracts/ssh-bootstrap/v1` 的 SuperToken ED25519 验签、SQLite nonce
  唯一登记和五分钟 OpenSSH 用户证书签发；同一 HTTPS listener 的精确
  `GET /` 提供无需认证的冻结只读状态/使用页；不进入 Block 本地业务依赖。
- `internal/mqttv2`：MQTTS/mTLS、QoS 0 发布、两个只读订阅和简单重连；
  不维护可靠投递状态。
- `internal/storage`：SQLite 本地快照、账号、报警历史、命令和审计数据。
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

模拟器配置文件路径必须是绝对路径；未知 JSON 字段会使进程启动失败。

```bash
./plc-simulator -config /etc/block/plc-simulator.json
```

`block-agent` 不再支持旧 JSON `-config` 启动方式。真机只由
`deploy/block/systemd/block.service` 启动，并显式传入本机 `8444` TLS、状态库、
HMI 静态目录、维护 HTTPS 和可选 MQTTS 参数。不要手工启动明文兼容 listener。

设备侧回归工具默认访问 `https://127.0.0.1:8444` 并严格校验公开 CA：

```bash
BLOCK_E2E_USERNAME=<approved-local-user> BLOCK_E2E_PASSWORD=<provided-at-runtime> \
  block-e2e --ca-file /usr/local/share/ca-certificates/block-dmp-blk-rel-001.crt
```

它拒绝 HTTP 和 `ws://`，并为 `/ws` 使用同源 WSS；不输出密码或保存凭据。

- 真实设备适配器尚未接入；非模拟部署使用 `adapter.type: "disabled"`。
- 只有实验环境使用
  `deploy/block/config/block-agent-simulator.example.json` 并启动 Simulator。
- `BLOCK_MQTTS_V2_ENABLED=false` 是独立运行默认值：不连接 BDM，本地采集、
  HMI、报警、历史和现场操作照常运行。
- 启用 MQTTS v2 时，仅通过 `deploy/block/config/block.env.example` 中的
  `BLOCK_MQTTS_V2_*` 环境变量传入端点、CA、Block 客户端证书/私钥、principal
  和稳定业务 ID。
- MQTTS v2 仅接受 `mqtts://HOST:8883`，使用 TLS 1.2/1.3，校验 CA、服务端
  主机名和客户端证书有效期、`clientAuth` EKU 及 CN 与 principal 的一致性。

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

本机 HMI 维护接口：

- `GET` / `PATCH /api/maintenance/production`：读取或更新目标产能、换刀件数、
  抽检间隔和单框工件数量；数据以原子 JSON 文件保存在 Agent 本地状态目录。
- `GET /api/maintenance/connectivity`：即时读取本机网卡、Wi-Fi 和 MQTTS v2
  连接状态。BDM 只返回 `not_configured` 或 `unknown`，不维护额外状态缓存。
- `POST /api/maintenance/wifi/connect`：仅接受本机 HMI 的 SSID 和当前密码，
  通过 NetworkManager 的 mode `0600` 临时 keyfile 应用连接。密码不会进入响应、
  日志或命令行参数，临时 keyfile 在调用结束后删除。

这些维护接口不实现 Cookie、角色或多账户认证；HMI 使用同机无状态认证 API 的
页面内存交互门禁。它们不提供 PLC 写入、BDM 控制、远程配置或任何 Pad 写接口。

## 本地认证

本地认证以 Common `contracts/block-local-api/2026-08-08` 为准。SQLite 是账号和页面空闲
时长的唯一来源：保存 username、Argon2id password hash、role 和 60–3600 秒的
`idleTimeoutSeconds`（默认 300）。不新增会话表。

- `GET` / `POST /api/auth/initial-admin`：读取首次管理员状态或创建首个 ADMIN。
- `POST /api/auth/login`：校验 username/password，返回 username、role、permissions。
- `POST /api/auth/password`：显式提交 username/currentPassword/newPassword 后改密。
- `GET` / `PUT /api/config/session`：读取或保存页面本地空闲时长。

这些接口不签发 Cookie、Token、JWT、`expiresAt` 或服务器登录会话；
`/api/auth/activity` 和 `/api/auth/logout` 不注册。角色映射只供 HMI 前端
控制按钮显示，Agent 不用其过滤业务、PLC 或维护接口。

### 2026-08-08 API 路径硬切换

本机 HMI 认证和维护只使用 `/api/...`。`/api/v1/...` 和
`/api/v2/...` 不提供别名、重定向或回退，请求统一返回 404。Common
`contracts/block-local-api/2026-08-08` 是当前契约制品目录，不代表 HTTP 路径版本。MQTTS v2 的协议、配置名称和运行语义不受此变更影响。

这些写操作只属于 Block 现场本地接口，不会从 BDM 或 Pad 调用。请求必须带
稳定 `Idempotency-Key`；Agent 在发送前写入 SQLite，按单队列执行，并在
独立读回确认后才标记 `EXECUTED`。传输结果不确定时记录 `UNKNOWN`，不会
盲目重发。报警确认只改变本地确认状态，不绕过急停、门禁或安全联锁。

## MQTTS v2 当前状态与报警历史

- MQTTS v2 仅用于当前状态和报警历史的只读同步；不接受远程控制、配置、升级
  或告警确认。
- 所有发布和订阅均为 QoS 0，且不使用 Retain、Outbox、应用层 ACK、Replay、
  Gap 账本或持久化 MQTT 会话状态。
- 网络断开时只保留内存中的最新当前状态；简单重连后重新发布最新状态，不补传
  中间状态。报警历史由 BDM 按查询接口读取。

## 持久化、失联和当前限制

- SQLite 使用 WAL、`synchronous=FULL` 和单连接串行写入。
- 保存最新快照、当前报警、报警历史、操作历史、命令结果和审计。
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
