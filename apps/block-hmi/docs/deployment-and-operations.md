# HMI 部署与运维

统一部署资料位于 `deploy/block/`。本文件只补充 HMI 侧约束。

## 必需配置

| 变量 | 值/示例 | 约束 |
| --- | --- | --- |
| `BLOCK_HMI_ADDR` | `127.0.0.1:8443` | 只能是此值 |
| `BLOCK_HMI_BASE_PATH` | `/block-apple-style` | 可配置合法的绝对子路径 |
| `BLOCK_HMI_AGENT_SOCKET` | `/run/block-agent/api/block-agent.sock` | 必须是绝对 Unix socket 路径 |
| `BLOCK_HMI_AGENT_TIMEOUT` | `8s` | 正 duration；默认 8 秒 |
| `BLOCK_HMI_TLS_CERT` | `/etc/block/certs/block-hmi.crt` | 必须是绝对路径 |
| `BLOCK_HMI_TLS_KEY` | `/etc/block/certs/block-hmi.key` | 必须是绝对路径，不得提交仓库 |

应用原生终止 TLS，最低版本为 TLS 1.2。没有明文监听器，也不做 HTTP 到 HTTPS 跳转。证书必须由本机浏览器/WebView信任，并包含访问名称或 `127.0.0.1` 的有效 SAN。

## 健康检查

```bash
BLOCK_HMI_CA=/etc/block/certs/ca.crt \
  /opt/block/current/deploy/health-check.sh
```

脚本对 HMI 使用受信 HTTPS，对 Agent 使用 Unix socket。Agent 检查中的 `http://localhost` 只是 Unix socket 内 HTTP 报文的占位 URL，不会建立 TCP 连接。禁止使用跳过证书校验的选项。

`/healthz` 只说明进程存活；设备数据是否可用必须再检查：

```bash
curl --fail --silent --show-error \
  --cacert /etc/block/certs/ca.crt \
  https://127.0.0.1:8443/api/v1/state
```

Simulator/Agent 断开时 HMI 进程健康检查仍为 `200`，而状态接口预期为带具体错误码的 `503`。首次可信读取失败时页面保持空值占位；已有可信读数后才显示最后更新时间，并始终禁用写操作。

## 发布门禁

- 记录版本、上一版本路径、配置摘要和 SQLite 备份位置。
- 验证 HMI 只监听 `127.0.0.1:8443`，Agent 与 Simulator 只创建预期 Unix socket。
- 验证 `80/1883/8080/8081` 未监听，明文请求失败且没有重定向。
- 使用正确 CA 成功访问；错误 CA、错误主机名、过期证书及 TLS 1.0/1.1 必须失败。
- 验证 SourceInfo 响应体、`X-Block-Source-Kind` 与 `X-Block-Simulation` 完整一致；Agent adapter 是来源类型的唯一权威。
- 非实验部署不得启用 `block-plc-simulator.service`，Agent 配置必须为 `adapter.type: "disabled"`，HMI 必须由 SourceInfo 生成非模拟标识。
- 实验部署的 Agent 必须为 `adapter.type: "simulator"`，HMI 必须由 SourceInfo 永久显示“模拟数据”，并验证 Simulator 断开/恢复、Agent 重启及 SQLite WAL 恢复。

## 故障处理

| 现象 | 检查 | 安全处理 |
| --- | --- | --- |
| 页面无法打开 | HMI service、证书路径、8443 监听 | 修复本机 HTTPS；不要临时打开明文端口 |
| 页面显示设备断开 | Agent socket、Agent 状态接口、数据新鲜度 | 恢复 Agent/适配器；不要重复发送结果未知的命令 |
| 写操作返回 `409 revision_conflict` | 当前 `revision` | 刷新后让操作员重新确认 |
| 写操作返回 `409 safety_interlock` | 急停、安全门和现场联锁 | 不刷新伪装成版本冲突；排除安全条件后再由操作员决定 |
| 写操作返回 `409 idempotency_conflict` | `Idempotency-Key` 是否被不同请求复用 | 不自动重发；生成新操作前先核对原操作 |
| 写操作返回 `503` | 快照是否 stale、质量、设备连接 | 保持写禁用，先恢复读取链路 |
| 写操作返回 `504 command_outcome_unknown` | HMI→Agent 取消或 8 秒超时、Agent 审计和设备现场状态 | 禁止自动重试；先核对设备与审计结果 |
| 重启后状态异常 | SQLite 主文件及 `-wal`/`-shm`、目录权限 | 停止 Agent，按 SQLite 一致性备份恢复 |
| 页面未显示模拟标识 | Agent adapter 与 SourceInfo 体/头一致性 | 停止实验，不得用未标识的模拟数据继续演示 |

## 回滚

1. 停止 `block-hmi` 和 `block-agent`；实验环境还要停止 `block-plc-simulator`。
2. 将 `/opt/block/current` 原子切换回已记录的上一版本；不要覆盖或删除当前 SQLite 数据。
3. 如果数据库迁移与上一版本不兼容，使用发布前的一致性备份恢复到单独路径，再把旧版本配置指向该副本。
4. 恢复上一版 systemd 与非秘密配置，执行 `systemctl daemon-reload`。
5. 先启动 Agent，再启动 HMI；实验环境按 Simulator、Agent、HMI 顺序启动。
6. 执行受信健康检查、状态读取、端口检查和一条经现场批准的低风险操作。

真实主机部署和回滚只能由 BLK-REL/设备管理员执行。本任务不连接或修改真实设备。
