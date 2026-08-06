# Block 本地 HMI v2

这个目录提供由 Block 运行时嵌入并向 Chromium Kiosk 提供的最小原生
TypeScript 页面。它不直接访问 PLC、BDM 或数据库，也不再启动独立 Go HMI
服务。

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

其中 Go 测试仅检查静态页面与 points.json；HTTP/WS 闭环由 block-agent 自身
测试承担。

## Apple Style V1 页面与本地预览

页面保留 Apple Style V1 的原始五页 DOM、内联样式、机器图、主题、时钟、
底部导航、软键盘和前端交互。旧 api-client.js 没有保留；assets/hmi.mts
是唯一业务实现来源，编译后生成 assets/hmi.mjs，并通过 v2 认证与 WebSocket
接口连接本机 Block。

无需 PLC 或 Block 真机的本地预览使用同一份页面：

~~~
python -m http.server 4173 --bind 127.0.0.1 --directory .
~~~

打开 http://127.0.0.1:4173/?demo=1。demo 使用固定数据，不请求
/api/v2/**，也不连接 /ws；主题、导航、toast、二次确认、软键盘和设备图
交互仍可用。

演示认证入口固定如下：

- `http://127.0.0.1:4173/?demo=1&auth=login` 是自动入口。首次（或浏览器
  无法读取演示标记）只显示创建管理员悬浮窗和常驻软键盘；创建成功后进入 HMI。
  同一浏览器随后再次打开该地址时，只显示普通登录悬浮窗和常驻软键盘。
- `http://127.0.0.1:4173/?demo=1&auth=bootstrap` 始终强制显示创建管理员
  页面，供测试首次安装页面使用。
- demo 只使用 `block-hmi-demo-admin-created-v1` 记录“管理员已创建”；不存储、
  不校验用户名、密码、哈希、Cookie 或会话，也不提供页面内的认证入口切换。

生产模式不带 demo=1。它显示本机登录/首次管理员覆盖层，保留修改密码、退出
和默认 300 秒不活动退出设置；认证后从 assets/points.json 配置 v2
runtime.configure，并通过 /ws 接收 points.snapshot 与 points.changed。
当前 v2 只映射 V1 的“启动”到已配置点位；生产统计、维护参数写入、报警确认和
历史记录没有后端能力时明确显示为暂无数据或不可操作。
