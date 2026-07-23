# Block 本地 HMI

该应用保留现有五页 Apple 风格触控界面和 `/api/v1/*` 路由，但生产 Controller 已改为只连接 Block Agent 的 Unix socket。HMI 不连接 Simulator、PLC、BDM 或网络控制面。

## 运行边界

- 浏览器入口固定为受信 HTTPS `127.0.0.1:8443`。
- 不监听、不跳转 `80`、`8080` 或 `8081`，也不提供明文 HTTP 模式。
- HMI 到 Agent 只使用配置的绝对 Unix socket 路径。
- Agent adapter 是数据来源的唯一权威。HMI 启动时校验 SourceInfo 响应体与 `X-Block-Source-Kind`、`X-Block-Simulation` 响应头完整且一致；不一致时拒绝启动。
- Agent 报告模拟来源时，页面永久显示醒目的“模拟数据”标识；该标识由首次可信握手结果渲染，不能被后续状态覆盖。
- 首次可信读取前页面只显示空值和“暂无可信数据”，不显示演示生产值或报警。已有可信读数后 Agent 断开或数据过期时，才保留最后读数并显示断连/过期时间，同时禁用所有业务写操作。
- 页面上的 `OPERATOR-01` 仍只是兼容显示文本，不代表已经实现身份认证或授权。

当前 HMI 公共状态字段和既有路由保持不变。`Idempotency-Key` 仅作为私有命令元数据透传给 Agent，没有加入请求体或公共响应。

## 构建与测试

```bash
go test ./...
go vet ./...
go test -race ./...

CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -trimpath -o /tmp/block-hmi .
```

测试包含受信证书成功、错误 CA、错误主机名、过期证书、旧 TLS 版本、明文请求拒绝、Unix socket 断开以及完整三进程闭环。

## 启动配置

```bash
export BLOCK_HMI_ADDR=127.0.0.1:8443
export BLOCK_HMI_BASE_PATH=/block-apple-style
export BLOCK_HMI_AGENT_SOCKET=/run/block-agent/api/block-agent.sock
export BLOCK_HMI_AGENT_TIMEOUT=8s
export BLOCK_HMI_TLS_CERT=/etc/block/certs/block-hmi.crt
export BLOCK_HMI_TLS_KEY=/etc/block/certs/block-hmi.key
./block-hmi
```

`BLOCK_HMI_ADDR` 必须严格等于 `127.0.0.1:8443`，证书和私钥路径必须为绝对路径。服务最低接受 TLS 1.2；健康检查必须使用可信 CA，禁止跳过证书验证。

实验闭环中只需把 Agent 明确配置为 `adapter.type: "simulator"`；HMI 从 Agent 的可信 SourceInfo 握手生成模拟标识。HMI 不提供独立的模拟开关，也不会自动回退到内存 Controller 或 Simulator。

## 文档

- [后端接线与兼容边界](docs/backend-integration.md)
- [部署、健康检查与回滚](docs/deployment-and-operations.md)
- 统一部署模板位于 `deploy/block/`；`apps/block-hmi/deploy/block-hmi.service` 是同内容的组件副本。

静态资源仍嵌入单一 Go 可执行文件；主题、软键盘和 Chromium/WebView 兼容逻辑未改变。第三方许可见 `THIRD_PARTY_NOTICES.md`。
