# Block

Block 是面向单台设备、可独立交付和独立运行的边缘产品。当前阶段一台 Block 对应一台设备；本地采集、状态、HMI、报警、历史和允许的现场操作不得依赖 Wi-Fi、BDM 或 Android Pad。

## 仓库范围

```text
apps/block-hmi/                 # Block 本地 TypeScript HMI
services/block-agent/           # 设备适配、状态、本地存储和 Local API
deploy/block/                   # Block 配置、systemd、安装与回滚资料
docs/requirements/              # Block 产品与 HMI 要求
docs/references/                # HMI 参考资料
tests/                          # 无 Wi-Fi / 无 BDM、单元与验收测试
archive/prototypes/web-hmi/     # 旧静态 HMI 原型，只读参考
```

当前功能基准由 `docs/development/Block-V2变更记录.md` 记录；真机版本与发布
结论只以受保护的发布报告为准。

## 公共架构与契约

公共架构和跨组件契约的唯一来源是工作区内的
`D:/codex/Block-DMP/repos/Common`。本仓库通过根目录的
`COMMON_BASELINE` 固定其确切 commit，不在本仓库复制一套可独立修改的公共契约。

修改 MQTT、OpenAPI、JSON Schema、身份字段或跨组件状态前，必须先由 `ARCH-COMMON` 更新 Common，再更新本仓库的 `COMMON_BASELINE`。

## 本地认证 v2

Block 本地认证契约位于 Common 的 `contracts/block-local-api/v2`。账号、角色、
Argon2id 密码哈希及页面空闲时长由 Block SQLite 保存；登录、首次设置和改密是
无状态请求，只返回 username、role、permissions。

- 不签发 Cookie、Token、JWT 或服务器登录会话；`/auth/activity` 与 `/auth/logout` 不存在。
- HMI 仅在页面内存中保存登录身份，并在真实 pointer、touch、keydown 时重置本地空闲计时器；刷新或设备重启回到访客态。
- ADMIN = operate + maintenance，OPERATOR = operate，VIEWER = 只读。该映射仅用于 HMI 交互，Block Agent 不用它过滤业务、PLC 或维护 API。

## 当前 HMI 基线

现有 [Block HMI](apps/block-hmi/README.md) 使用 TypeScript 运行时和同机 Local API v2；
演示模式也使用同一认证 API 与测试 SQLite 数据库。正式实现必须：

- 只调用 Block Local API，不直连 BDM；
- 不绕过 `block-agent` 直接访问 PLC；
- 在无 Wi-Fi、无 BDM 时正常工作；
- 同机通信使用 Unix socket 或 TLS；
- 不部署明文 `8080/8081`。

## 持续变更记录

- [Block V2 变更记录](docs/development/Block-V2变更记录.md)：当前源码、真机版本、验证结论与下一步。

## 安全

- 真实 `wifi.toml`、`.env`、密码、私钥、证书私钥和现场配置不得进入仓库。
- 只提交 [wifi.example.toml](deploy/block/wifi.example.toml) 这类明确占位样例。
- APK、数据库、日志、构建缓存和目标机二进制不得作为源码提交。
- 真实 `192.168.1.101` 只能由 `BLK-REL` 执行写操作；本仓库开发默认只在本地进行。
