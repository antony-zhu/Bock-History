# SSH Bootstrap v1 验收

`verify.ps1` 从 Block 的 `COMMON_BASELINE` 读取固定 Common commit，并以
`git show <baseline>:contracts/ssh-bootstrap/v1/...` 提取该 commit 的契约向量；它绝不使用
Common 当前 HEAD。该验证是可选的；需要用户自行 clone Common 到任意位置并显式传入
`-CommonRoot`，正式 `build-release.ps1` 不调用它。脚本运行 pinned 契约、Block Go 测试、`go vet -buildvcs=false -mod=readonly`、
race，并执行 `GOENV=off GOWORK=off GO111MODULE=on`、空 `GOROOT`、
`CGO_ENABLED=0 GOOS=linux GOARCH=arm64 GOARM64=v8.0`、`-buildvcs=false` 的
`ssh-bootstrapd` 静态构建。它调用根目录统一 bootstrap 下载并校验固定 Go/Node，按
`go.mod`/`go.sum` 在指定 state root 中准备模块；不使用 `.tools` 或历史 `GOMODCACHE`。
所有继承的 `GIT_CONFIG_*` 注入在 Git 与构建命令前清除，但 `HTTP(S)_PROXY` 和正常 Git
代理配置仅影响下载路径，不记录其值且不绕过 SHA-256、lock 或 Go checksum 校验。

```powershell
.\tests\ssh-bootstrap\verify.ps1 `
  -StateRoot '.cache\ssh-bootstrap-verify' `
  -CommonRoot '<path-to-Common-checkout>'
```

正式 release 一键构建不需要 C 工具链；但本脚本的 `go test -race` 会启用 cgo，必须有可用的
本机 `gcc` 或 `clang`。缺少时脚本会明确失败。仅做普通构建诊断时可显式加 `-SkipRace`，但该
结果不构成 SSH race 门禁。所有临时目录、Go 构建缓存、模块、工具链和二进制都落在带 owner
marker 的 state root，不能提交。

Go 测试覆盖：

- 无认证精确 `GET /` 的冻结 HTML、精确 Content-Type、动态值转义、禁用内容
  排除，以及其他 GET 路径 `404`；
- 正常签发、严格 300 秒 TTL、唯一 `release/debug` principal；
- SuperToken v1 固定向量、method/path/原始正文/身份篡改；
- `-60/+60` 秒接受、`-61/+61` 秒拒绝；
- SQLite nonce 原子唯一登记、边界秒和重启持久性；
- 跨 Block/Device/节点、root profile 和非 ED25519 公钥拒绝；
- TLS 正确 CA、错误 CA、过期证书、错误主机名、TLS 1.1 和明文拒绝；
- 成功/错误响应字段固定且不含秘密；
- 管理服务不依赖 BDM、Wi-Fi 或 Block 本地业务运行时。

`GET /` 与原有 `POST /v1/ssh/cert` 共用同一个 TLS-only listener；状态页不
改变 POST 的 SuperToken、请求/响应、错误码、时间窗、nonce 或签发行为。

部署静态/回滚入口位于
`deploy/block/ssh-bootstrap/verify-static.sh` 和同目录 README。真实主机签发
与登录只能由 `BLK-REL` 按受控步骤执行。
