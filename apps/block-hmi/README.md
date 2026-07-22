# Apple 风格 Block 本地 HMI

这是现有料仓 HMI 的独立视觉版本，保留五个页面、模拟控制、参数设置、报警确认、历史记录及自动/手动切换逻辑。
界面提供浅色、石墨、海蓝、午夜、钛金五套可切换皮肤，并会记住最近一次选择。深色皮肤统一使用白色按钮外框和白色文字，同时保留状态色的语义。

兼容性目标为 Chromium/WebView 57 及以上。处理包括 `vh/vw` 回退、Grid/Flex 间距回退、旧版键盘焦点、低性能模式、无 Popover API 的普通按钮菜单、旧主题存储值迁移，以及对动画、可选全屏 API 和元素查找的安全降级。

- 入口：`index.html`
- 设备图片：`assets/machine-bin.png`
- 运行方式：完整功能使用内置 Go 服务；任意静态文件服务器只适合视觉预览
- 线上路径：`https://www.antonyzhu.com/block-apple-style/`

当前 Controller 仅用于界面和接口联调，不连接 PLC、MES 或其他生产系统。配置数据文件后，修改后的演示状态和审计可由后端持久化，生产状态与设备动作仍为演示实现。

## 产品边界

- 本项目定位为同一台 Block 内部使用的本地 HMI 和 `Block Local API` 原型，不是 BDM API，也不是 Android Pad API。
- 当前阶段一台 Block 对应一台设备；Block 本地功能必须在没有路由器、Wi-Fi、BDM 或 Pad 时正常运行。
- Android Pad 不得把 `apiBase` 指向本地 Block API，也不得逐台直连 Block 或 PLC；正式 Pad 只通过 BDM 只读查看数据和告警。
- BDM 和 Pad 第一阶段不远程控制 Block。当前页面的启动、暂停、复位、清仓等写操作仅代表 Block 本地演示交互。
- 所有真实业务 TCP/IP 通信必须使用 HTTPS/MQTTS/WSS。同机 HMI 与 Agent 优先使用 Unix socket；如使用 loopback TCP 也必须 HTTPS/mTLS。明文端口不监听、不跳转并直接拒绝。

## 文档

- [前后端架构、API、操作流、审计与 PLC 接入](docs/backend-integration.md)
- [构建、部署、持久化、监控与回滚](docs/deployment-and-operations.md)

身份认证和权限控制尚未实现；页面中的 `OPERATOR-01` 只是演示文字，不代表已登录用户。生产环境接入前请先完成认证、权限和真实 PLC Driver。

## Go 服务

页面和图片通过 `embed` 编进一个可执行文件，不需要在工控机上安装 Go 或复制散落的静态文件。

```bash
go test ./...
go build -trimpath -ldflags="-s -w" -o block-hmi-server .
./block-hmi-server
```

当前代码只实现明文 HTTP，开发默认值为 `0.0.0.0:8080`。它是已知非合规原型，只能在隔离开发环境联调，不能作为生产发布物。正式 Block HMI 目标为受信 HTTPS `127.0.0.1:8443`，HMI→Agent 使用 Unix socket 或 HTTPS/mTLS，并在验证后关闭 `8080/8081`。`192.168.1.101` 当前仍监听明文 `0.0.0.0:8080`，已列入下一次受控发布整改项。TLS 实现前不得通过“只绑定 loopback”宣称合规。

设置 `BLOCK_HMI_DATA_FILE` 后，服务会原子保存修改后的演示状态和审计；未设置时仅保存在进程内存中：

```bash
BLOCK_HMI_DATA_FILE=/var/lib/block-hmi/state.json ./block-hmi-server
```

为 ARM64 Linux 工控机构建：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -trimpath -ldflags="-s -w" -o block-hmi-server .
```

systemd 服务模板位于 `deploy/block-hmi.service`，当前设备从 `/opt/block-hmi/bin/current` 启动。该模板仍对应 HTTP 原型，必须在实现 TLS/Unix socket 后一并升级，当前不得复用于生产部署。

## 可选软键盘模块

维护页默认启用触控数字键盘，可在页面中切换为系统 / 实体键盘；选择会保存在本机浏览器。URL 可用 `?keyboard=soft` 或 `?keyboard=native` 临时覆盖。

模块也内置四行 QWERTY、大小写和符号布局。后续登录输入框添加 `data-soft-keyboard="full"` 后，调用 `window.HMISoftKeyboard.refresh()` 即可复用；登录键可用 `data-soft-done-label="登录" data-soft-submit="true"` 配置。密码仍使用 `type="password"`，键盘面板不显示明文，关闭或切换字段时会清理组件内部缓存。数字字段使用 `data-soft-keyboard="numeric"`。若不需要此模块，将根元素的 `data-soft-keyboard-enabled` 改为 `false` 即可安全回退原生输入。

横屏紧凑布局已为左右安全区预留 `env(safe-area-inset-*)`，所有新增触控目标不小于 44px。正式接入登录层时，登录输入区应放在全键盘上方的可见区域；鉴权、会话和维护审计仍由后端模块负责，不在本静态输入组件中保存凭据。

项目自带固定版本 `simple-keyboard@3.8.165`，不访问 CDN。第三方许可见 `THIRD_PARTY_NOTICES.md` 与 `assets/vendor/simple-keyboard/LICENSE`。
