# Block 本地 HMI v2

`assets/hmi.mts` 是 HMI 的唯一业务源码，`assets/hmi.mjs` 是浏览器加载的
编译产物。页面只通过同机 `/ws` 获取点位快照和变化；它不直接访问 PLC、BDM
或数据库。

## 访客与本地管理员

页面打开后立即进入访客 HMI，并从同机 Block Agent 读取初始管理员状态和空闲时长，
同时建立 WebSocket。访客可以查看主页、数据、报警和历史；访问维护页、现场操作、
报警确认和 PLC 管理会打开本地登录窗口，不会发送对应的运行时命令。

本地账号由 Block Agent 的 SQLite 数据库管理，密码使用 Argon2id 哈希。认证 API 只
校验请求并返回用户名、角色和前端权限摘要：不签发 Cookie、Token 或后端登录会话。

- 首次安装通过 `POST /api/v2/auth/initial-admin` 创建唯一初始管理员；成功后页面仅在内存中进入当前登录态。
- `POST /api/v2/auth/login`、`POST /api/v2/auth/password` 都显式提交用户名和密码；改密还必须提交当前密码。
- 页面身份、角色、权限和空闲倒计时都不写浏览器持久化存储。刷新、关闭浏览器或设备重启后回到访客态。
- `GET`/`PUT /api/v2/config/session` 持久化 60 到 3600 秒的页面空闲时长，默认 300 秒；指针、触控和键盘活动只重置本地计时器，不访问活动续期接口。
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

- 生产参数的目标、换刀件数和抽检间隔在编辑停止 650 ms 后通过本机
  `PATCH /api/v2/maintenance/production` 保存；单框工件数量单独保存。
- Wi-Fi 状态从本机 `GET /api/v2/maintenance/connectivity` 读取；连接请求发送到
  `POST /api/v2/maintenance/wifi/connect`。密码只用于当前请求，提交后立即清空，
  不会回显。
- PLC 页只显示现有 WebSocket 的连接、最近采样/错误、点数和实时点值，并保留
  既有的扫描、连接、断开和刷新操作；不提供 PLC 网络或点表手工配置。
- 账号页使用 Block Agent 的本地认证 API 修改密码和页面空闲时长；维护接口不引入
  Cookie、角色校验或多账户管理 API。

`?demo=1` 在浏览器内模拟上述维护值和 Wi-Fi 状态，不会请求或配置真实网络。

## PLC 连接

手动连接成功后，Block Agent 在本地状态目录保存 PLC endpoint。运行时收到完整
点位表后仅在已有保存 endpoint 时自动连接；没有保存地址时不扫描、不连接。
手动断开会清除保存地址。该生命周期属于 Agent，HMI 不使用 `localStorage`
保存或恢复 PLC 地址。

## 本地验证

有 TypeScript 编译器时更新浏览器产物：

```text
tsc assets/hmi.mts --target ES2022 --lib DOM,ES2022 --module NodeNext --moduleResolution NodeNext --strict --skipLibCheck --noEmitOnError --outDir assets
```

执行验证：

```text
node assets/hmi.test.mjs
go test ./...
```

`assets/points.json` 是页面、点位和显示绑定的唯一来源。`runtime.configure`
只发送运行时点位；显示路径和中文说明只在浏览器使用。
