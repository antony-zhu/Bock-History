# SSH Bootstrap v1 验收

`verify.ps1` 固定读取 Common
`contracts/ssh-bootstrap/v1`，运行契约向量、Block Go 测试和 vet，并执行
`CGO_ENABLED=0 GOOS=linux GOARCH=arm64` 的 `ssh-bootstrapd` 静态构建。
所有临时目录、Go 构建缓存和二进制都落在工作区 `.cache/**`。

Go 测试覆盖：

- 正常签发、严格 300 秒 TTL、唯一 `release/debug` principal；
- SuperToken v1 固定向量、method/path/原始正文/身份篡改；
- `-60/+60` 秒接受、`-61/+61` 秒拒绝；
- SQLite nonce 原子唯一登记、边界秒和重启持久性；
- 跨 Block/Device/节点、root profile 和非 ED25519 公钥拒绝；
- TLS 正确 CA、错误 CA、过期证书、错误主机名、TLS 1.1 和明文拒绝；
- 成功/错误响应字段固定且不含秘密；
- 管理服务不依赖 BDM、Wi-Fi 或 Block 本地业务运行时。

部署静态/回滚入口位于
`deploy/block/ssh-bootstrap/verify-static.sh` 和同目录 README。真实主机签发
与登录只能由 `BLK-REL` 按受控步骤执行。
