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
创建后显示登录页；`?demo=1&auth=bootstrap` 始终显示创建页。

前端权限仅负责本机 HMI 的交互门禁。Block Agent 仍负责点位表、Mask Write、
PLC 连接状态及安全校验，并对同机 HMI 返回相同的运行时数据。

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
