# 2026-08-08 Block API 清理版真机发布报告

## 结论

发布与回归结果为 **PASS**。设备当前版本为 `0.0.0-api-cleanup-20260808`，当前
release 为 `/opt/block/releases/0.0.0-api-cleanup-20260808`。现行无版本 Local API、
严格 TLS/WSS、HMI 布局、数据库清理和 PLC 短回归均通过。

本报告不包含账号内容、密码、私钥、真实配置内容或可复用的设备管理凭据。

## 1. 发布方式与清理结果

- 发布源码 HEAD 为 `051523d60dc5aa13f26336cf99817a0a7975d71f`。
- 直接创建新 release 并原子切换 `/opt/block/current`；现有 systemd unit、Block 配置和
  Chromium policy 未改动。
- 未调用会创建 snapshot 或 previous 信息的 installer，未创建新 previous 指针或持久
  snapshot，也未执行回滚演练。
- 候选 archive、临时构建目录和传输脚本均未保留。
- 本地临时目录 `.cache/direct-device-release-20260808` 已删除。
- 设备临时 stage
  `/var/backups/block/stage-direct-api-cleanup-20260808T052416Z` 已删除。
- 按开发阶段规则，本轮未做程序备份或数据库备份。

## 2. 数据库迁移

新 Block Agent 启动时执行 `006_cleanup.sql`，删除四张已退役 MQTT v1 可靠同步表：

| 退役表 | 发布前 | 发布后 |
| --- | --- | --- |
| `mqtt_outbound_inflight` | 存在，0 行 | 不存在 |
| `uplink_gap_ledger` | 存在，0 行 | 不存在 |
| `uplink_outbox` | 存在，0 行 | 不存在 |
| `uplink_stream_state` | 存在，0 行 | 不存在 |

数据库在发布前、发布后及 PLC 短回归后的 `PRAGMA integrity_check` 均为 `ok`。业务表
行数保持不变：`local_accounts=1`、`local_system_settings=1`、
`current_snapshot=1`、`active_alarms=2`、`alarm_history=2`、
`alarm_history_v2=2`、`operation_history=0`、`command_records=0`、
`audit_records=0`。未读取、输出、创建或删除账号。

## 3. API、TLS、WSS 与 HMI

HTTP 探测使用严格 CA 校验且不跟随重定向：

| 探测 | 结果 |
| --- | --- |
| `GET /healthz` | `200`，无 `Location` |
| `GET /api/auth/initial-admin` | `200`，无 `Location` |
| `POST /api/auth/login`（固定错误凭据） | `401` 业务错误，无 `Location` |
| `GET /api/config/session` | `200`，无 `Location` |
| `GET /api/maintenance/production` | `200`，无 `Location` |
| 代表性 `/api/v1/...` | `404`，无重定向 |
| 代表性 `/api/v2/...` | `404`，无重定向 |

严格 WSS `/ws` 返回 `HTTP/1.1 101 Switching Protocols`。错误 CA、错误主机名和
TLS 1.1 及以下均连接失败；`8444` 仅监听 `127.0.0.1`，`80`、`1883`、`8080`、
`8081` 无业务监听，`8443` 与 `9443` 状态不变。

已提交的 1920×1080 与软键盘登录布局 probe 通过。设备 installed web 与本地发布
web 共 13 个文件，逐文件 SHA-256 为 **13/13 一致**；设备画面正常渲染并显示“后台
联机”。

## 4. PLC 短回归

设备沿用正式 PLC endpoint；`runtime.configured` 确认 `scanIntervalMs=50`，通信记录
持续出现 `FC03 address=504 quantity=1`。

| 操作 | 结果 |
| --- | --- |
| D504.4 / `machine.enabled` toggle | `false → true`，`9.109 ms` |
| 恢复原值 | `true → false`，`7.810 ms` |
| D504.1 / `machine.startCommand` 100 ms pulse | 往返 `111.819 ms`，最终自动回落 `false` |
| D504.2 / `machine.startFeedback` | 全程保持 `true` |
| 邻位 | `machine.jogForward=true`、`machine.enabled=false`，均恢复初始状态 |
| 最终 D504 | `12` |

测试结束时，PC 模拟器 PID 为 `21584`，管理页为
`http://127.0.0.1:8875`，Modbus TCP 为 `192.168.1.87:1502`、Unit `1`，
`activeClients=1`。这些是测试结束时状态，不代表永久运行状态；需要停止该进程时执行：

```powershell
Stop-Process -Id 21584
```

## 5. 服务与证据

发布完成后 `block.service`、`block-kiosk.service`、`ssh-bootstrapd.service` 均为
`active`，启动健康检查通过。

原始证据位于工作区缓存目录（缓存不进入 Git）：

- `.cache/device-api-cleanup-release-20260808/final-report.md`
- `.cache/device-api-cleanup-release-20260808/device-screen.png`
- `.cache/device-api-cleanup-release-20260808/plc-short.log`

未连接或探测 BDM，未尝试外部 MQTT broker。无 BDM 时，本地 HMI 与 PLC 闭环正常。
