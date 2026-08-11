# Block 本地 HMI v2

`assets/hmi.mts` 是 HMI 的唯一业务源码，`assets/hmi.mjs` 是浏览器加载的
编译产物。页面只通过同机 `/ws` 获取点位快照和变化；它不直接访问 PLC、BDM
或数据库。

生产与真实演示均使用同源 `https://127.0.0.1:8444`。页面只建立
`wss://127.0.0.1:8444/ws`，不会降级为 `ws://`；认证和维护 API 保持同源相对
路径。8080/8081 没有 HMI 兼容监听、重定向或明文回退。

## 访客与本地管理员

页面打开后立即进入访客 HMI，并从同机 Block Agent 读取初始管理员状态和空闲时长，
同时建立 WebSocket。访客可以查看主页、数据、报警和历史；访问维护页、现场操作、
报警确认和 PLC 管理会打开本地登录窗口，不会发送对应的运行时命令。

本地账号由 Block Agent 的 SQLite 数据库管理，密码使用 Argon2id 哈希。认证 API 只
校验请求并返回用户名、角色和前端权限摘要：不签发 Cookie、Token 或后端登录会话。

- 首次安装通过 `POST /api/auth/initial-admin` 创建唯一初始管理员；成功后页面仅在内存中进入当前登录态。
- `POST /api/auth/login`、`POST /api/auth/password` 都显式提交用户名和密码；改密还必须提交当前密码。
- 页面身份、角色、权限和空闲倒计时都不写浏览器持久化存储。刷新、关闭浏览器或设备重启后回到访客态。
- `GET`/`PUT /api/config/session` 持久化 60 到 3600 秒的页面空闲时长，默认 300 秒；指针、触控和键盘活动只重置本地计时器，不访问活动续期接口。
- 角色映射只服务 HMI 交互门禁：ADMIN 可操作和维护，OPERATOR 可操作，VIEWER 只读；Block Agent 不用该前端状态拦截业务或 PLC API。
- 退出或超时后立即回到访客态，但不会关闭 WebSocket 或清空已显示的现场数据。左下角显示“登录”或当前用户名；登录后点击当前用户名直接退出。

`?demo=1` 仍使用相同的 Block Agent 认证 API 和测试 SQLite 数据库，不提供第二套
浏览器内存认证数据；`?demo=1&auth=login` 按后端初始管理员状态显示相应认证页，
`?demo=1&auth=bootstrap` 用于直接预览创建页。演示入口会自动使用
固定 `1920×1080` 的 iframe 画布：在工控机分辨率下按原尺寸显示，较小的电脑窗口
只会等比缩小并居中，不会触发内部页面的响应式重排。演示壳会保留原始查询参数，
并且只在 iframe 内加载一次实际 HMI。

前端权限仅负责本机 HMI 的交互门禁。Block Agent 仍负责点位表、Mask Write、
PLC 连接状态及安全校验，并对同机 HMI 返回相同的运行时数据。

## 维护页

维护页固定为四个本机 tab：生产参数、Wi-Fi、PLC 通信和账号管理。每个 tab
独立滚动，所有输入都可使用软键盘。

- 今日目标产能从 PLC `D1000` 读取。生产数据页只显示该值；仅维护 → 生产参数可写入
  `D1000`。所有生产参数均在对应输入框的软键盘点“完成”后保存。
- Wi-Fi 状态从本机 `GET /api/maintenance/connectivity` 读取；连接请求发送到
  `POST /api/maintenance/wifi/connect`。密码只用于当前请求，连接成功后清空，
  不会回显。
- PLC 页仅保留子网、地址、端口和 Unit ID、连接状态、候选，以及扫描、连接、刷新和
  断开操作；点表不在页面编辑。仅 PLC IP 输入框使用带 `.` 键的专用小数软键盘，其他
  数字输入仍保留 `00` 键。
- 账号页使用 Block Agent 的本地认证 API 修改密码和页面空闲时长；维护接口不引入
  Cookie、角色校验或多账户管理 API。

PLC 值未读、stale、error 或断线时，相关数值显示“—”。

`?demo=1` 在浏览器内模拟上述维护值和 Wi-Fi 状态，不会请求或配置真实网络。

## PLC 连接

手动连接首次读取成功后，Block Agent 在同一个 `block.db` SQLite 中覆盖保存唯一
PLC endpoint（IP、端口和 Unit ID，deviceId 由其生成），不保存点表或点值。运行时
收到完整点位表后仅在已有保存 endpoint 时自动连接；没有保存地址时不扫描、不连接。
普通手动断开只断开当前会话，保留该记录，因此下次上电仍会自动连接。该生命周期属于
Agent，HMI 不使用 `localStorage` 保存或恢复 PLC 地址，也不再写 `plc-endpoint.json`。

## 本机真实演示（开发）

在 Block 工作树根目录执行：

```powershell
.\tools\start-block-hmi-auth-demo.ps1
```

该工具构建并启动实际的 `services/block-agent/cmd/block-agent`，以
`https://127.0.0.1:8444` 提供此目录的 HMI 静态文件，并以同源 WSS 提供
`/ws`。未传入证书参数时，它只在仓库根目录
`.cache/block-hmi-auth-demo/tls/` 生成短期开发 CA、叶子证书和私钥；这些文件
不会进入仓库，也不适用于真机。脚本会调用根目录的统一构建 bootstrap，在同一
state root 中准备并校验 Go 和模块；不读取 `.tools` 或历史模块缓存。演示构建期间会
清除继承的 `GIT_CONFIG_*`、Node TLS 覆盖和 npm 配置，并在启动成功或失败后恢复调用
PowerShell 原有的环境变量，不把工具缓存路径或隔离设置泄漏给后续命令。需要为演示
指定独立 state root 或使用指定证书时：

```powershell
.\tools\start-block-hmi-auth-demo.ps1 `
  -StateRoot '.cache\block-hmi-demo'

.\tools\start-block-hmi-auth-demo.ps1 `
  -TLSCertificatePath C:\path\local-hmi.crt `
  -TLSPrivateKeyPath C:\path\local-hmi.key `
  -TLSCAPath C:\path\local-hmi-ca.crt
```

首次不传 TLS 文件时，演示还明确依赖系统可用的 OpenSSL（Git for Windows 自带的
OpenSSL 可用）；没有 OpenSSL 时必须显式提供上述三份 TLS 文件。state root 只能是仓库根目录
`.cache` 的直接子目录并带本工具 owner marker，不能使用 `D:\state`、工作树或 junction。

默认复用
`.cache/block-hmi-auth-demo/state/block-hmi-auth-demo.db`，因此可验证账户和
idle 配置的重启持久化；只有显式传入 `-FreshAuth` 才会删除这个精确的开发演示状态目录。首次
运行时该目录不存在或为空会安全初始化；只有已有且非空的目录才要求 demo owner marker，避免清除
外部数据；`-Stop` 仅会停止 PID 记录和命令行都匹配的本工作树 Agent。

```powershell
.\tools\test-block-hmi-auth-persistence.ps1
```

第二个脚本使用独立的 `.cache/block-hmi-auth-persistence-test` 数据库、随机回环
HTTPS 端口和严格 CA 校验，覆盖首次管理员、登录、idle 配置、无 Cookie、已退役
认证端点、同库重启持久化、WSS 地址及没有 8080/8081 明文业务监听；结束时清理其测试目录。

## 本地验证

根目录的正式构建入口会从锁定依赖重编译本目录的 `assets/hmi.mts` 到独立 state
root，并逐字节比对现有 `assets/hmi.mjs`；不一致即失败，构建过程不会改写这个已
跟踪的浏览器运行时资产。它同样验证 PLC simulator 的 `web/app.ts`/`web/app.js`，
运行 Node/Go 测试，并构建 Linux ARM64 release：

```powershell
Set-Location <Block-repository-root>
.\tools\build-release.ps1 -Version <approved-unique-version>
```

首次执行自动下载并 SHA-256 校验 Go `1.26.5` 与 Node.js `24.14.0`，并使用根
`package-lock.json` 中精确锁定的 TypeScript `5.6.3`；正式 `build-release.ps1` 不依赖
系统 PATH 中的 Go、Node 或 TypeScript，也不使用任何历史 `.cache`。它仍明确要求
PowerShell 与 Git for Windows；直接调用 `deploy/block/build.sh` 则必须自行提供
`BLOCK_GO_BIN` 和完整受控 Go 环境。

`assets/points.json` 是默认/真实 PLC 使用的获批点表，派生自
`D:\PLC_Points\PLC_Points_260809.xlsx`；当前包含 **42 个**运行时点位：**33 个 BOOL**、1 个
FLOAT32/REAL、5 个 INT16 和 3 个 INT32/DINT，`scanIntervalMs=500`。其中 27 个为原有
非报警点位（18 个 BOOL、1 个 FLOAT32/REAL、5 个 INT16、3 个 INT32/DINT），另有 15 个
只读 BOOL 报警点位；15 条报警定义不等于已完成真机验证。遗留的
`assets/points.simulatorFloat32.json` 仅为显式选择的 legacy 模拟 profile；默认页面始终加载
`points.json`。`runtime.configure` 只发送所选点表的运行时点位；显示路径和中文说明只在
浏览器使用。

自动运行速度 D522 当前仅供读取和显示；主页的 0–300 滑条始终禁用，并显示读取值的百分比。

默认点表中的所有 DINT/REAL 均使用 `wordOrder: "low-high"`：低 16 位寄存器在前，高 16 位
寄存器在后（D522、D902、D904、D1000）。运行时仍支持显式配置的 `high-low` 兼容 profile。

## 2026-08-08 模式切换与显示文案

- 主页库位状态区只渲染库位1和库位2；它们等宽同排显示，后端保留的其余库位状态不删除也不在主页显示。
- 页面不再向现场用户展示 `V2` 或 `v2` 标识；认证和维护统一使用 `/api/...`。
  `/api/v1/...` 和 `/api/v2/...` 不保留兼容层，也不重定向；MQTTS v2 不受此路由切换影响。
- 访客点击自动/手动模式会打开本地登录。ADMIN、OPERATOR 登录且 PLC 已连接后，HMI
  通过 `home.machine.enabled` 绑定的 `machine.enabled` toggle 发送既有 WSS
  `point.command`；不新增后端命令或 PLC 写入路径。
- 自动/手动显示始终由 PLC 点位回传的当前状态决定：自动为绿色，手动为黄色。命令
  成功、失败、超时和断线均通过页面 toast 反馈，不使用浏览器原生对话框。
