# Block SSH Bootstrap v1 部署

本目录部署独立 `ssh-bootstrapd` 管理服务。它只在 HTTPS `9443/tcp` 提供
`POST /v1/ssh/cert`，不会监听或跳转明文 HTTP，也不改变 Block Agent、
HMI、MQTTS、PLC 或 SSH `22/tcp` 的既有业务/管理职责。服务不可用时，
Block 的本地采集、状态、HMI、报警、历史和现场操作不受影响。

实现绑定 Common
`contracts/ssh-bootstrap/v1`，管理员私钥和客户端 SSH 私钥都不进入目标
节点。目标只保存管理员 ED25519 验签公钥、本机 HTTPS 密钥以及本机独立
SSH 用户 CA。Block CA 不得复制给 BDM。

## 文件与账户

```text
/opt/ssh-bootstrap/releases/<version>/
/opt/ssh-bootstrap/current
/etc/ssh-bootstrap/config.json
/etc/ssh-bootstrap/admin-ed25519-public.pem
/etc/ssh-bootstrap/tls/server.crt
/etc/ssh-bootstrap/tls/server.key
/etc/ssh-bootstrap/tls/ca.crt
/etc/ssh-bootstrap/ssh-user-ca
/etc/ssh-bootstrap/ssh-user-ca.pub
/opt/ssh-bootstrap/principals/release
/opt/ssh-bootstrap/principals/debug
/var/lib/ssh-bootstrap/nonces.db
```

`ssh-bootstrap` 是锁定密码、无登录 shell 的非 root 服务用户。
`release`、`debug` 是锁定密码的非 root SSH 用户。sshd drop-in 只信任本机
CA，并从 `%u` 对应文件读取唯一同名 principal；没有 root principal 文件。
principals 目录固定为 root-owned `0755` 的
`/opt/ssh-bootstrap/principals`，避免异常的 `/etc` 父目录权限导致
OpenSSH `StrictModes` 拒绝证书登录。

安装器在修改 sshd 配置前先探测目标 daemon 是否支持 `Include`。支持时使用
`/etc/ssh/sshd_config.d/60-ssh-bootstrap.conf`；Ubuntu 18.04 现场的
OpenSSH 7.6 不支持时，安装器在完整备份原 `sshd_config` 后，向文件尾部追加
唯一的 `BEGIN/END SSH-BOOTSTRAP MANAGED BLOCK`。重复安装复用已有的完全匹配
block，不会重复追加。两种模式都在 reload 前执行 `sshd -t`，回滚精确恢复
原 `sshd_config`（包括原本不存在）、principals、drop-in、unit、服务状态和
`current`，且不读取或修改任何 `authorized_keys`。
现有 SSH 密码/密钥策略不由本部署修改。

## 构建和静态验证

SSH Bootstrap 代码/契约验证是可选入口，不直接依赖 PATH 中的 Go、Node、TypeScript 或历史模块
缓存；它要求用户显式提供包含 `COMMON_BASELINE` 固定 commit 的独立 Common checkout。正式
`build-release.ps1` 不调用该验证，也不依赖 Common：

```powershell
.\tests\ssh-bootstrap\verify.ps1 `
  -StateRoot '.cache\ssh-bootstrap-verify' `
  -CommonRoot '<path-to-Common-checkout>'
```

该入口使用 bootstrap/state root 下载并校验工具和模块，执行带 `-buildvcs=false` 的 test、vet
和 Linux ARM64 静态构建。默认 race 检查会启用 cgo，因此需要本机 `gcc` 或 `clang`；只有
普通构建诊断时才可显式 `-SkipRace`，其结果不构成 race 门禁。

裸 `go build` 只可用于已准备好同一受管 state root、显式 Go 环境的局部诊断，不能作为正式
制品或契约验证入口。`./verify-static.sh` 会优先使用调用方传入的 `BLOCK_NODE_BIN`（可指向统一
bootstrap 准备的已校验 Node）；只有独立运行静态检查时才回退到 PATH 中的 `node`。

所有测试缓存和产物必须放在仓库根目录 `.cache/**`。样例只含占位指纹和公开
路径；不得提交真实 `.env`、管理员私钥、TLS 私钥、SSH CA 私钥、密码或
现场配置。

## 受控安装

Release 先记录当前 unit、端口、sshd 配置摘要和备份位置，然后在 Block
本机生成独立 ED25519 SSH 用户 CA，准备管理员公钥、HTTPS 证书/私钥和已
审查配置。只有 `BLK-REL`/设备管理员可执行：

```bash
sudo env BLOCK_RELEASE_ROLE=BLK-REL ./install.sh \
  --execute \
  --version 1.0.0 \
  --artifact-dir /secure/artifact \
  --config /secure/staging/ssh-bootstrap.json \
  --tls-cert /secure/staging/server.crt \
  --tls-key /secure/staging/server.key \
  --tls-ca /secure/staging/https-ca.crt \
  --admin-public-key /secure/staging/admin-ed25519-public.pem \
  --ssh-ca-private /secure/staging/block-ssh-user-ca \
  --ssh-ca-public /secure/staging/block-ssh-user-ca.pub \
  --health-host 192.168.1.104 \
  --git-commit <full-block-commit> \
  --common-baseline <full-common-commit>
```

DNS 证书增加 `--server-name <dns-name>`。安装器校验管理员公钥类型、TLS
公私钥匹配、SSH CA 公私钥匹配、配置精确字段、`sshd -t`、systemd 状态、
版本与受信 TLS。安装成功后，Release 还必须用离线管理员私钥执行一次真实
SuperToken v1 请求，使用 `ssh-keygen -L` 核对唯一 principal、Key ID 和
严格 300 秒 TTL，再按响应的 `hostKeyFingerprint` 严格校验主机密钥并登录
专用用户。健康脚本不接收也不保存管理员私钥。

管理网络策略只允许受控维护端访问 `9443/tcp`。不得开放 HTTP 端口或将其
重定向到 HTTPS。`22/tcp` 继续作为 ADR-003 调试管理例外。

## 回滚

```bash
sudo env BLOCK_RELEASE_ROLE=BLK-REL ./rollback.sh --execute
```

回滚停止并禁用 `ssh-bootstrapd`，恢复安装前备份的 unit、配置、证书、
sshd drop-in 和 `current` 链接，执行 `sshd -t` 后 reload。现场防火墙中的
`9443/tcp` 放行由 Release 按同一变更记录撤销。nonce 数据库和 release
文件保留到回滚确认后再按现场记录处理，不删除 Block 本地业务数据。
安装器在首次替换受管文件前原子记录本次事务；后续 `sshd -t`、SSH reload、
服务启动或健康检查失败时，会自动用该事务恢复安装前状态。此流程不读取、
删除或覆盖任何用户的 `authorized_keys`。

## v1 明确未实现

限流、自动封禁、mTLS、复杂审计平台、HSM、多管理员、复杂密钥轮换和降级
认证不属于 v1。部署不得自行添加这些行为，也不得弱化 HTTPS、严格验签、
±60 秒窗口、SQLite nonce 唯一登记、独立 CA 和秘密保护。
