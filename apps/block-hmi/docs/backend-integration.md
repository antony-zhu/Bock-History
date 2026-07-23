# HMI 后端接线与兼容边界

## 架构

```mermaid
flowchart LR
    Browser["本机浏览器 / WebView"] -->|"受信 HTTPS 127.0.0.1:8443"| HMI["block-hmi"]
    HMI -->|"Unix socket /run/block-agent/api/block-agent.sock"| Agent["block-agent"]
    Agent -->|"Unix socket；仅实验模式"| Simulator["plc-simulator"]
```

HMI 的生产 Controller 只访问 Agent。它不会连接 Simulator 或 PLC；Simulator 控制 socket 也不会暴露给页面。内存 Controller 只保留为单元测试夹具，不存在生产启动回退路径。Agent adapter 是来源类型的单一权威；HMI 没有第二套模拟模式开关。

HMI 首次连接必须从 `/internal/v1/source` 同时获得完整响应体、`X-Block-Source-Kind` 和 `X-Block-Simulation`。响应体与两个响应头必须逐项一致，否则 HMI 拒绝启动。首次取得可信状态前，页面只显示空值、空报警列表和“暂无可信数据”占位。

本任务复用既有 HMI 路由、请求体、响应字段和错误语义。Agent 内部的 `commandId`、数据质量、Simulator 会话和采样序号都被隔离在适配层内，没有成为公共字段。

## 浏览器可见 API

页面同源访问以下路径；默认也可在 `/block-apple-style` 前缀下访问：

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| `GET`/`HEAD` | `/api/v1/state` | 读取完整状态；新鲜且质量为 `GOOD` 时返回 `200` |
| `PUT` | `/api/v1/settings` | 修改 `target`、`toolLimit`、`inspectInterval` |
| `POST` | `/api/v1/commands` | 本地设备命令 |
| `POST` | `/api/v1/alarms/{id}/ack` | 确认报警，不复位安全联锁 |
| `GET`/`HEAD` | `/api/v1/audit` | 审计分页 |
| `GET`/`HEAD` | `/healthz` | HMI 进程存活；不代表 Agent/设备可用 |

`state` 对象字段固定为：

```text
revision, updatedAt, running, mode, singlePaused, framePaused,
target, output, cycle, oee, inspected, passed, ng, pending,
blank, finished, toolLimit, inspectInterval, bins, alarms, history
```

写请求继续使用 `X-Operator`、`X-Request-ID`、可选 `expectedRevision`/`If-Match`。浏览器适配器为每次用户操作生成稳定的 `Idempotency-Key`，HMI 原样转发给 Agent；该值不加入 JSON 请求体或响应。

支持的命令保持不变：

- `start`、`pause`、`reset`、`clear_bins`、`inspect`
- `set_mode`，附带 `mode: "auto" | "manual"`
- `set_single_paused`、`set_frame_paused`，附带 `paused: boolean`

设置和命令成功响应仍为：

```json
{
  "state": {},
  "message": "操作结果"
}
```

审计响应仍为 `items` 和可选 `nextBeforeId`。现有 `HMIState` JSON 标签由测试精确锁定，防止无意增加、删除或改名字段。

## 可用性和命令语义

- Agent 的快照过期、连接断开或数据质量不是 `GOOD` 时，`GET state` 分别返回 `503 data_stale`、`503 device_unavailable` 或 `503 bad_quality`；Agent 传输层不可用才使用 `503 backend_unavailable`。
- 同一条件下所有写操作均返回对应的具体错误码；HMI 立即禁用写按钮。只有已经取得过可信状态时才保留最后成功状态并显示过期/断连提示。
- Agent 以单写队列执行命令，收到结果后还会独立读回状态。只有读回与命令预期一致才返回成功。
- HMI 到 Agent 的变更请求只要发生 context 取消或 8 秒传输超时，就保守返回 `504 command_outcome_unknown`，不返回 `backend_unavailable`。是否已被 Agent 接纳无法安全证明，HMI 和 Agent 都不会自动重发此类命令。
- 浏览器请求默认 12 秒超时，并使用 `AbortController` 取消底层 `fetch`。浏览器超时同样按“结果未知、禁止自动重试”提示。
- 相同 `Idempotency-Key` 在 Agent 重启后仍返回已保存结果，不会再次执行。
- `expectedRevision` 过期返回 `409 revision_conflict`，这是唯一会触发页面立即刷新并让操作员重新确认的写错误。`409 safety_interlock` 和 `409 idempotency_conflict` 均显示各自原因，不得伪装成 revision 冲突。

主要错误保持既有包络：

```json
{
  "error": {
    "code": "backend_unavailable",
    "message": "设备数据已过期或连接中断"
  }
}
```

常用错误码包括 `malformed_json`、`operator_required`、`validation_error`、`revision_conflict`、`safety_interlock`、`idempotency_conflict`、`alarm_not_found`、`device_unavailable`、`bad_quality`、`data_stale`、`command_outcome_unknown`、`method_not_allowed`、`not_found` 和 `backend_unavailable`。

## 模拟模式标识

首次 SourceInfo 可信握手报告 `kind: "simulator"` 且 `simulation: true` 时，HMI 服务端把“模拟数据”标识写入页面根元素。标识锁定于该次握手结果，因此设备断开、刷新或状态恢复都不会让它消失；运行中来源头改变时 HMI 以 `source_mismatch` 失败关闭。启用或停用实验模拟只修改 Agent adapter 配置。

## 尚未实现

本任务没有实现真实 PLC Driver、登录/会话/角色权限、MQTT/BDM、Pad、远程控制、企业服务器、MES、AGV 或 OTA。`OPERATOR-01` 不能作为授权依据；真实设备接入前仍需独立完成身份认证、权限和现场安全评审。
