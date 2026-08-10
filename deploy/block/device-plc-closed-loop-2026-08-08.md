# 2026-08-08 Block HMI 与 PC 模拟 PLC 真机闭环测试报告

## 结论

测试结论为 **PASS**。设备运行版本为 `0.0.0-hmi-mode-race-20260808`，Block 通过
有线 `eth0` 连接 PC 模拟 PLC，完成正式 WSS 配置、50 ms 扫描、外部值变化通知、
自动/手动模式切换和 100 ms 脉冲闭环。测试结束时三个设备服务均为 `active`，kiosk
已恢复正常。

本文件是 2026-08-08 的历史记录：其中 50 ms 参数和证据只描述当时版本。当前源码使用每次完整读取结束后等待 500 ms 的轮询节奏，需另行验证，不能由本报告推断。

本报告只记录可公开的版本、网络、协议和测试结果，不包含账号内容、密码、私钥、
真实配置内容或可复用的设备管理凭据。

## 1. 版本与变更

- 用户可见页面中的 `V2`/`v2` 文案已移除；`/api/v2`、MQTTS v2 等技术接口、配置和
  契约标识保留。
- `98faf42`（`fix(block-hmi): restore PLC-backed mode toggle`）恢复自动/手动 HMI
  切换，并保持 PLC 回显作为页面状态来源。
- `3989672`（`fix(block-hmi): reserve point command before send`）在发送前预留单个
  在途点位命令，修复快速操作时的竞态。
- 测试 Git HEAD 为 `3989672799077514350f0915589b00d453550ec1`，包含上述两项修复。
- 设备版本为 `0.0.0-hmi-mode-race-20260808`。

制品核验结果：

| 项目 | 结果 |
| --- | --- |
| 制品压缩包 SHA-256 | `5AED43CB716B1A80C279A3ECD11968D594A55A924B9EEFAA84ED881BE72D3690` |
| manifest SHA-256 | `06480F4FC7A98DBECE0A8C0D231DE60C9B71EC51FBAFB868373DE46A0E4CC902` |
| `block-agent` SHA-256 | `45A7B9A2EE2DE391ED2D5900A043C984808834AD9A41D32DEC00CFBDEFC6BABF` |
| manifest | 30 项全部匹配 |
| 架构 | AArch64 ELF |

## 2. PC 模拟 PLC

模拟器使用仓库中的 `tools/plc-simulator`。本次点表导入路径和状态快照路径为：

- 点表导入：`D:\codex\Block-DMP\.cache\plc-device-test-20260808\simulator_points-run-20260808T022655Z.json`
- 状态快照：`D:\codex\Block-DMP\.cache\plc-device-test-20260808\simulator_state-run-20260808T022655Z.json`

模拟器保持最小协议面：Unit ID `1`，FC03 读取保持寄存器，FC22 执行单 bit 掩码写入
并回显请求；FC06、FC16 和其他功能码返回 `Illegal Function (01)`。FC22 使用当前字、
AND mask 和 OR mask 计算新值，只改变目标 bit，保留同一 D 字的相邻位。

模拟器测试为 **17/17 PASS**（`Ran 17 tests in 3.097s`），覆盖 FC22 掩码回显、置位、
清位、未掩码位保持、非法请求拒绝和并发写入等行为。100 ms 脉冲时序由 Block
负责，模拟器没有为该脉冲设置 timer。

测试结束时的运行状态如下。这些值是测试结束时的快照，不应当作永久状态：

| 项目 | 测试结束时状态 |
| --- | --- |
| 模拟器进程 | PID `21584`，`python` |
| Modbus TCP | `192.168.1.87:1502` |
| 本机管理页/API | `http://127.0.0.1:8875` |
| 活动客户端 | `1` |
| 最终请求计数 | `11131` |
| 最终 D504 | `12` |

无需继续测试时，在 PC 上停止该次模拟器进程：

```powershell
Stop-Process -Id 21584
```

本次未创建 Windows 防火墙规则。

## 3. 直连网络路径

PC 有线地址为 `192.168.1.87/24`。设备 `eth0` 原地址为
`169.254.53.232/16`；确认 `192.168.1.105` 无地址冲突后，临时增加以下非持久化
地址和主机路由：

```sh
ip addr add 192.168.1.105/24 dev eth0 noprefixroute
ip route replace 192.168.1.87/32 dev eth0 src 192.168.1.105 metric 10
```

最终 PLC 路由为 `192.168.1.87 dev eth0 src 192.168.1.105`。PLC 业务数据只经过
有线 `eth0`；未修改默认路由和 WLAN。SSH 仍作为调试控制入口，因此本结果不是“设备
完全断 Wi-Fi”测试，但 PLC 闭环本身不依赖 BDM，也不经过 Wi-Fi。

上述地址和路由在设备重启后会消失。无需继续测试时清理：

```sh
ip route del 192.168.1.87/32 dev eth0
ip addr del 192.168.1.105/24 dev eth0
```

## 4. 正式 WSS 配置

通过本机严格 TLS WSS 正式接口配置并连接 PLC：

- `scanIntervalMs=50`，`runtime.configured` 返回 `50 ms`；
- Easy521 endpoint 为 `easy521://192.168.1.87:1502?unitId=1`；
- `plc.connect.result` 返回 `success=true`、`state=connected`；
- kiosk 恢复后自动加载该 endpoint，并保持一个活动 Modbus 客户端；
- 通信记录持续出现 `FC03 address=504 quantity=1`，即一次读取 D504 中的多个 bit，
  不是逐 bit 读取。

## 5. PASS 矩阵

| 测试项 | 结果 | 现场观测 |
| --- | --- | --- |
| FC03 同字多位读取 | PASS | `address=504 quantity=1`，一次读取 D504 多个位。 |
| 模拟器 HTTP 改值到 Block WSS | PASS | D504.2 `false → true`，WSS 收到 `points.changed machine.startFeedback=true`。 |
| `machine.enabled` 第一次 toggle | PASS | D504.4 `false → true`，命令往返 `6.577 ms`。 |
| `machine.enabled` 第二次 toggle | PASS | D504.4 `true → false`，命令往返 `6.971 ms`。 |
| toggle 邻位保持 | PASS | D504.1=false、D504.2=true、D504.3=true 均未改变。 |
| `machine.startCommand` 100 ms pulse | PASS | 写 D504.1、读 D504.2；往返 `107.859 ms`，写位最终自动回落为 false。 |
| 读写点分离 | PASS | 写点 D504.1，反馈读点 D504.2；反馈保持 true。 |
| 最终寄存器 | PASS | D504=`12`，即 D504.2 与 D504.3 为 true。 |
| 服务与健康检查 | PASS | `block.service`、`block-kiosk.service`、`ssh-bootstrapd.service` 均 active；严格 TLS HTTPS/WSS、策略和端口检查通过。 |

## 6. FC22 证据边界

模拟器只保留 100 条滚动通信明细。50 ms FC03 扫描很快覆盖了四次现场 FC22 明细，
因此本次没有保留下逐条现场 FC22 mask 日志；按照停止指令没有重放操作，也不把缺失
明细描述为已采集证据。

闭环结论由以下已保留证据共同支撑：Block `point.command` 返回结果、两次 toggle
往返时间、100 ms 脉冲往返时间、最终 D504 和邻位状态，以及模拟器 17/17 协议测试。
协议测试已覆盖 FC22 高低变化可观察、掩码回显、非法请求拒绝和未掩码位保持。

## 7. 可复现步骤与证据

1. 使用 `tools/plc-simulator` 启动 Unit 1，Modbus 绑定
   `192.168.1.87:1502`，管理页绑定 `127.0.0.1:8875`，并使用第 2 节的点表文件。
2. 按第 3 节添加设备临时 `eth0` 地址和 `/32` 路由。
3. 通过正式 WSS 设置 `scanIntervalMs=50`，保存第 4 节 Easy521 endpoint 并执行
   `plc.connect`。
4. 核对 FC03 `address=504 quantity=1`；通过模拟器 HTTP 修改 D504.2，并在 WSS
   核对 `machine.startFeedback=true`。
5. 分别执行两次 `machine.enabled` toggle 和一次 `machine.startCommand` 100 ms
   pulse，核对命令结果、往返时间、邻位、读写点和最终 D504。
6. 核对三个服务、严格 TLS HTTPS/WSS、端口和 kiosk；完成后按第 2、3 节命令停止
   模拟器并清理临时网络配置。

原始证据位于工作区缓存目录（缓存不进入 Git）：

- `.cache/plc-device-test-20260808/formal-api-plc-connect.log`
- `.cache/plc-device-test-20260808/closed-loop-harness.log`
- `.cache/plc-device-test-20260808/simulator-baseline.json`
- `.cache/plc-device-test-20260808/http-point-change.json`
- `.cache/plc-device-test-20260808/simulator-communication-after-commands.json`
- `.cache/plc-device-test-20260808/simulator-unittest.log`

## 8. 备份与最终状态

本轮处于开发阶段，没有破坏性数据库写操作，因此未做程序备份或数据库备份。
发布前后数据库完整性均为 `ok`，本地账号数量均为 `1`；仅核对数量，没有读取或输出
账号内容。

测试结束时设备运行 `0.0.0-hmi-mode-race-20260808`，三个服务均 active，kiosk 已
恢复，PLC endpoint 已由正式接口保存并自动连接。模拟器和临时有线配置在测试结束时
仍保留，分别按第 2、3 节命令清理。
