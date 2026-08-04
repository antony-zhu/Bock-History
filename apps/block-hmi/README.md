# Block 本地 HMI v2

这个目录提供由 Block 运行时嵌入并向 Chromium Kiosk 提供的最小原生
TypeScript 页面。它不直接访问 PLC、BDM 或数据库。

## 页面配置

assets/points.json 是页面、点位和显示绑定的唯一来源：

- points 是登录后以 runtime.configure 发送给后端的完整运行时点表。
- bindings 与 layout 只在浏览器中使用，不会发送给后端。
- 每个 displayPath 使用小写英文点路径；每个 description 为中文。
- 页面只显示后端通过 points.snapshot 和 points.changed 给出的 PLC 绝对值。

控制按钮只发送通用 point.command：

- pulse：后端按点表中的默认 100 ms 执行；
- momentary：前端在 pointer down/up 时发送 press/release；
- toggle：后端使用新鲜 PLC 反馈决定目标值。

浏览器不提前修改点位值、不重试或缓存命令。实际 pointer、touch 或 keyboard
输入才会请求 /api/v2/auth/activity；渲染、WebSocket 消息和重连定时器不会
续期。

## 本机验证

TypeScript 源码是 assets/hmi.mts，提交的 assets/hmi.mjs 是供浏览器加载的
产物。使用已有 TypeScript 编译器时可执行：

~~~
tsc assets/hmi.mts --target ES2022 --lib DOM,ES2022 --module NodeNext --moduleResolution NodeNext --strict --skipLibCheck --noEmitOnError --outDir assets
node assets/hmi.test.mjs
go test ./...
~~~
