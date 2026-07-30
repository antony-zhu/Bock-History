# SSH Bootstrap v1 验收

`verify.ps1` 固定读取 Common
`contracts/ssh-bootstrap/v1`，运行契约向量、Block Go 测试、vet 和 race，并执行
`CGO_ENABLED=0 GOOS=linux GOARCH=arm64` 的 `ssh-bootstrapd` 静态构建。
所有临时目录、Go 构建缓存和二进制都落在工作区 `.cache/**`。

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
