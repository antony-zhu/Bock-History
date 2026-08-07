# Block 本地 HMI v2

`assets/hmi.mts` 是 HMI 的唯一业务源码，`assets/hmi.mjs` 是浏览器加载的
编译产物。页面只通过同机 `/ws` 获取点位快照和变化；它不直接访问 PLC、BDM
或数据库。

## 访客与本地管理员

页面打开后立即进入访客 HMI 并尝试建立 WebSocket。访客可以查看主页、数据、
报警和历史；访问维护页、现场操作、报警确认和 PLC 管理会打开本地登录窗口，
不会发送对应的运行时命令。

本地管理员完全由前端管理：

- 管理员用户名、SHA-256 密码摘要和固定权限标记保存于 `localStorage`；不保存明文密码。
- 登录态保存于 `sessionStorage`，默认 300 秒无活动退出；指针、触控和键盘活动续期。
- 退出或超时后立即回到访客态，但不会关闭 WebSocket 或清空已显示的现场数据。
- 左下角显示“登录”或当前管理员；登录后点击当前管理员直接退出。
- 修改密码、会话超时和 PLC 控制位于“维护”页面。浏览器不会保存 PLC 地址。

`?demo=1` 是访客演示；`?demo=1&auth=login` 在未创建本地管理员时显示创建页、
创建后显示登录页；`?demo=1&auth=bootstrap` 始终显示创建页。演示入口会自动使用
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
- 账号页仍只使用浏览器本地管理员、密码修改和会话超时设置；维护接口不引入
  Cookie、角色或多账户 API。

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
