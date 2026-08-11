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

## 可复现构建

在 Windows x64 的新 clone 中，唯一正式构建入口是：

```powershell
.\tools\build-release.ps1 -Version <approved-unique-version>
```

正式构建要求 Git 工作树没有已修改或未跟踪文件；它会把提交、Git tree 和 clean 状态写入
`artifact/build-metadata.json`。`-AllowDirty` 只用于开发诊断，不能产生归档或发布 hash。

前置条件是 Windows PowerShell、Git 命令行和 Git for Windows（提供 `bash`、`cygpath`、
`file`、`grep`）；未传 `-StateRoot` 时，正式入口会为已校验的 `-Version` 使用仓库根目录
`.cache/block-release-<version>/`，不同版本绝不共享该目录，并在其中下载并 SHA-256 校验官方
Go `1.26.5` 与 Node.js `24.14.0`，从根 `package-lock.json` 安装精确的
TypeScript `5.6.3`，从 `go.mod`/`go.sum` 下载并验证 Go 模块，再以离线模块模式
生成 Linux ARM64 release。它不需要系统 PATH 中的 Go、Node 或 TypeScript，也不依赖
任何历史 `.tools`、`.cache` 或模块缓存；Git for Windows 仍是正式入口的明确系统前置。
脚本先从 PATH 中的 `git.exe` 推导 Git for Windows 根目录；非标准安装可显式传
`-GitBash 'X:\Git\bin\bash.exe'`，或设置同名含义的 `BLOCK_GIT_BASH`，无需安装在固定目录。

需要为一次验证明确指定临时位置时，可增加 `-StateRoot`：

```powershell
.\tools\build-release.ps1 -Version <approved-unique-version> `
  -StateRoot '.cache\my-clean-build'
```

State root 只能是仓库根目录 `.cache` 的直接子目录，脚本会写入 owner marker 并拒绝 junction、
仓库根、卷根和非本工具拥有的非空目录。需要强制重建一个已拥有的 state root 时才加
`-FreshState`；它不会删除无 marker 的目录。该目录含工具链、模块、npm、临时文件和
`artifact/`，不进入 Git；发布前按 [`deploy/block/README.md`](deploy/block/README.md)
完成独立的制品校验和设备授权。

直接运行 `tools/bootstrap-build-tools.ps1` 而不传 `-StateRoot` 时，默认使用通用的
`.cache/block-build/`；它适合单独的工具准备，不是正式 release 的默认目录。正式
`build-release.ps1` 始终先按版本选择独立目录再调用 bootstrap。

bootstrap 固定 `GOENV=off`、`GOWORK=off`、`GO111MODULE=on` 并清除 `GOROOT`，使已校验
的 portable Go 自行解析；同时清除 `NODE_OPTIONS`、`NODE_PATH`、
`NODE_TLS_REJECT_UNAUTHORIZED`、`NODE_EXTRA_CA_CERTS`、`NODE_USE_SYSTEM_CA`、继承的
`NPM_CONFIG_*` 以及所有继承的 `GIT_CONFIG_*` 注入。不会记录或清除 `HTTP(S)_PROXY` 与正常 Git
代理配置：它们只影响下载传输路径，工具 SHA-256、`package-lock`、`go.mod/go.sum` 和
`sum.golang.org` 仍校验下载内容，代理值不会写入 metadata 或报告。

清理前先在用户终端审阅精确清单；默认不会删除任何文件：

```powershell
.\tools\cleanup-build-artifacts.ps1 -StateRoot '.cache\my-clean-build'
# 确认输出后才执行：
.\tools\cleanup-build-artifacts.ps1 -StateRoot '.cache\my-clean-build' -Execute
```

## 固定构建和 HMI/应用烧写

日常构建和烧写均使用固定脚本；“烧写”只指 Block HMI/应用，不会配置 Wi-Fi、PLC 点位或向 PLC 写入。

```powershell
# 构建（唯一正式构建逻辑仍由 build-release.ps1 执行）
.\tools\build-block.ps1 -Version <approved-version>

# 先检查烧写命令，不连接设备、不构建、不写入
.\tools\deploy-hmi.ps1 -Version <approved-version> -Build `
  -DeviceAddress <device-address> -SiteId <site-id> -BlockId <block-id> -DeviceId <device-id> -DryRun

# 正式烧写：脚本构建、打包、HTTPS 获取五分钟 SSH 证书、上传候选制品、安装并验收
.\tools\deploy-hmi.ps1 -Version <approved-version> -Build `
  -DeviceAddress <device-address> -SiteId <site-id> -BlockId <block-id> -DeviceId <device-id> `
  -CommonRoot <pinned-Common-checkout> -AdminKid <administrator-kid> `
  -AdminKey <protected-administrator-key> -ManagementCA <protected-management-ca>
```

若已有受批准的 ssh-bootstrapctl.exe，可用 -BootstrapCtl 取代 -CommonRoot；若已由受保护工具生成有效会话，可用 -SessionDirectory 取代三个管理员凭据。凭据、会话和现场配置始终保留在仓库外。

部署脚本需要 WSL 来用 Linux tar 保留候选制品的执行位。它先检查 HTTPS/SSH 身份、受验 host key、候选哈希、远端 sudo -n 权限和设备现有配置，再只运行候选制品内的 install.sh，随后运行 verify-install.sh。任一步失败都会非零退出并保留不含秘密的诊断；不会尝试密码、跳过主机校验或重复烧写。

无需设备的命令组装检查：

```powershell
.\tools\test-deploy-hmi.ps1
```

## 公共架构与契约

公共架构和跨组件契约的唯一来源是独立的 Common Git checkout。本仓库通过根目录的
`COMMON_BASELINE` 固定其确切 commit，不在本仓库复制一套可独立修改的公共契约。正式
`build-release.ps1` 不需要 Common；只有可选的 SSH Bootstrap 契约验证需要用户显式传入
`-CommonRoot <path-to-Common-checkout>`，且该 checkout 必须包含 `COMMON_BASELINE` 固定的 commit。

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
