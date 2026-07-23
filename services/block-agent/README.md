# Block Agent 与 PLC Simulator

`BLK-SIM-001` 提供完全本机、可离线运行的开发闭环：

```text
plc-simulator -- /run/block-plc/io/io.sock -- block-agent
block-agent   -- /run/block-agent/api/block-agent.sock -- block-hmi
```

两段进程间通信都使用 Unix socket。Simulator 和 Agent 不监听 TCP，不读取 Wi-Fi 配置，也不连接 BDM、MQTT、Pad、MES、AGV 或真实 PLC。当前 Simulator 是语义级设备模型，不模拟任何厂商寄存器或现场总线。

## 组件

- `cmd/plc-simulator`：确定性生产节拍、合格/不合格计数、料仓、报警、安全联锁、命令执行、故障注入和重启持久化。
- `cmd/block-agent`：采样、数据新鲜度、单写命令队列、`commandId` 幂等、SQLite WAL、本地报警/历史/审计以及 HMI 兼容适配层。
- `internal/plccontract`：仅在 Block 仓库内部使用的私有语义协议 `block-plc-private/v1`，不是 Common 公共契约。

## 构建与测试

```bash
go test ./...
go vet ./...
go test -race ./...

CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -trimpath -o /tmp/block-agent ./cmd/block-agent
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -trimpath -o /tmp/plc-simulator ./cmd/plc-simulator
```

SQLite 驱动为纯 Go 实现，因此 Linux ARM64 构建保持 `CGO_ENABLED=0`。

## 本地启动

所有配置文件路径必须是绝对路径；未知 JSON 字段会使进程启动失败。

```bash
./plc-simulator -config /etc/block/plc-simulator.json
./block-agent -config /etc/block/block-agent.json
```

非模拟部署必须使用 `adapter.type: "disabled"`。只有实验环境才复制 `deploy/block/config/block-agent-simulator.example.json` 并启动 Simulator。身份必须由配置显式提供，不能用 IP 或主机名代替 `siteId`、`blockId`、`deviceId`。

## Unix socket API

Simulator I/O socket：

- `GET /healthz`
- `GET /v1/snapshot`
- `POST /v1/commands`

Simulator 控制 socket（仅实验故障注入）：

- `POST /v1/faults`
- `POST /v1/restart`

可注入断开、响应延迟、冻结数据、`GOOD`/`UNCERTAIN`/`BAD` 质量、急停、门禁打开、严重报警、命令拒绝、命令失败以及“已执行但响应超时”。控制 socket 不暴露给 HMI。

Agent 本地 API socket：

- `GET /healthz`
- `GET /api/v1/state`
- `PUT /api/v1/settings`
- `POST /api/v1/commands`
- `POST /api/v1/alarms/{id}/ack`
- `GET /api/v1/audit`

Agent 适配现有 HMI 路由、字段和语义，没有新增或改名公共字段。写请求必须带稳定的 `Idempotency-Key`；Agent 在发送前写入 SQLite，按单队列执行，并在独立读回确认后才标记 `EXECUTED`。如果响应超时或传输结果不确定，则记录 `UNKNOWN`，不会自动重发。

## 持久化与失联行为

- SQLite 启用 WAL、`synchronous=FULL` 和单连接串行写入。
- 保存最新快照、当前报警、报警历史、操作历史、命令结果和审计；Outbox 表仅预留，本任务没有实现 MQTT/BDM。
- Simulator 保存计数器、设置和已处理命令 ID；进程重启生成新的会话 ID，序号在会话内单调递增。
- Simulator 断开后 Agent 继续存活并保留最后快照；超过 `staleAfter` 后状态读取返回 `503 backend_unavailable`，HMI 禁止写操作。
- Agent 重启会恢复 SQLite 快照和幂等记录；崩溃前仍为 `PENDING` 的命令恢复为 `UNKNOWN`，不会盲目重发。
- 报警确认只改变确认状态，不绕过急停、门禁或其他安全联锁。

部署、健康检查和回滚见 `deploy/block/README.md`。
