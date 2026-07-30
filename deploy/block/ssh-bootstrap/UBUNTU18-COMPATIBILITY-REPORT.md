# Block Ubuntu 18.04 sshd 兼容修复验证报告

| 项目 | 结果 |
|---|---|
| 修复基线 | `62da092dec3ec3f35474fcf3544709ddb1ff3197` |
| 现场系统 | Ubuntu 18.04、OpenSSH 7.6 |
| 原故障 | 顶层 `Include` 被 `sshd -t` 以 `Bad configuration option` 拒绝 |
| 实施范围 | 仅 `deploy/block/ssh-bootstrap/**`，未执行真机部署 |
| 结果 | PASS |

安装器现在先用目标 `sshd -t -f` 探测 `Include` 能力。支持时继续使用
drop-in；不支持时在完整事务备份后向原 `/etc/ssh/sshd_config` 尾部追加一个
带稳定 BEGIN/END 标记的 managed block。重复应用保持单一 block。两种模式
均配置：

```text
TrustedUserCAKeys /etc/ssh-bootstrap/ssh-user-ca.pub
AuthorizedPrincipalsFile /opt/ssh-bootstrap/principals/%u
```

`/opt/ssh-bootstrap/principals` 固定为 `root:root 0755`。安装和回滚都在
reload 前运行 `sshd -t`；事务回滚覆盖原 `sshd_config` 的存在/不存在状态、
principals 目录、drop-in、unit、服务状态和 `current`，不接触 root 或其他
用户的 `authorized_keys`。

## 本地门禁

- `deploy/block/ssh-bootstrap/tests/deploy-regression.sh`：PASS
- `deploy/block/ssh-bootstrap/verify-static.sh`：PASS
- 全部相关 Shell 脚本 `bash -n`：PASS
- Ubuntu 18.04 无 Include fixture、inline 重复安装：PASS
- `sshd -t` 失败和安装中断后的两模式精确回滚：PASS
- Block Agent Go test/vet 与 Linux ARM64 `ssh-bootstrapd` 构建：PASS

所有临时目录和构建产物均位于工作区 `.cache/**`。
