# Block 原生部署与回滚

本目录处理“一台 Block 对应一台设备”的本机闭环，并提供可选的 BDM
只读数据上行。Block 的采集、状态、HMI、报警、历史和允许的现场操作不依赖
Wi-Fi、BDM 或外部服务器。启用 BDM 时，Block 仅作为 MQTTS 客户端主动连接
BDM；本目录不部署 MQTT Broker、Pad、远程命令、远程升级或真实 PLC Driver，
也不会连接或修改远程主机。

真实 Block 上的写操作只允许 `BLK-REL`/设备管理员执行。开发智能体只能做源代码和静态验证。

短期 SSH 用户证书签发是独立管理面，部署入口位于
`ssh-bootstrap/README.md`。它绑定 Common `contracts/ssh-bootstrap/v1`，
只监听 HTTPS `9443/tcp`，不并入本地业务发布事务，也不成为 Agent/HMI 的
启动或运行依赖。

## 支持基线

- Ubuntu 18.04.5 LTS。
- systemd 237。
- Bash 4.4、GNU coreutils、curl、OpenSSL、Python 3、util-linux、iproute2。
- 三个静态非 root 服务账户，不使用 `DynamicUser`。

用户创建使用严格幂等的 `useradd/groupadd/usermod`，并强制服务账户密码处于锁定状态；没有依赖不同发行版可能采用不同默认策略的 sysusers 配置。unit 只使用 systemd 237 已支持的沙箱项；没有采用较新 systemd 才支持的 `ProtectProc`、`ProcSubset` 或 `SocketBindAllow`。

## 权威路径和通信

| 主体 | 允许访问 | 明确禁止 |
| --- | --- | --- |
| `block-hmi` | Agent API `/run/block-agent/api/block-agent.sock`；HMI 证书 | Simulator、Agent SQLite |
| `block-agent` | 自有 SQLite；实验时 Simulator I/O `/run/block-plc/io/io.sock`；可选 BDM 客户端证书和到 BDM `8883/tcp` 的 MQTTS 出站 | Simulator control；任何业务入站 TCP |
| `block-simulator` | 自有 I/O/control socket 和状态 | Agent API、Agent SQLite |
| 显式实验管理员 | 加入 `block-sim-control` 后访问 control socket | 不因该组获得其他服务权限 |

访问组只有：

- `block-hmi-api`：`block-agent`、`block-hmi`；
- `block-sim-io`：`block-agent`、`block-simulator`；
- `block-sim-control`：`block-simulator` 和发布命令中显式列出的实验管理员。

不存在共享的 `block` 组。三个服务各用自己的主组，socket 均为 `0660`。Simulator control 不向 Agent 或 HMI 开放。

HMI 只监听 `127.0.0.1:8443`，且只能使用受信 HTTPS。`80`、`1883`、
`8080`、`8081` 不监听、不跳转。Simulator 只使用 Unix socket，并保持
`PrivateNetwork=true` 和 `AF_UNIX`。Agent 不监听业务 TCP；其 unit 允许
`AF_UNIX`，并只允许向当前实验 BDM `192.168.1.105/32` 出站。Agent 不依赖
`network-online.target`，BDM 不可达不会阻止本地服务启动。HMI unit 使用
`IPAddressDeny=any`、`IPAddressAllow=localhost`，阻断外连。SSH `22/tcp`
的调试例外不属于业务程序。

健康检查中的 `http://localhost` 只作为 curl 访问 Unix socket 时必需的占位 URL，不经过 TCP。

## 生产与实验模式

生产是安全默认值：

- Agent 配置使用 `adapter.type: "disabled"`；
- Simulator unit 被 `disable --now`，也不安装 Simulator 二进制。
- 删除遗留的 `/etc/block/plc-simulator.json`。

实验模式必须在安装命令中显式使用 `--profile lab`：

- Agent 使用 `adapter.type: "simulator"` 和 `/run/block-plc/io/io.sock`；
- 安装 Simulator 二进制和非秘密配置；
- 按 Simulator → Agent → HMI 顺序启用和启动。

HMI 请求 Agent 的固定超时是 `8s`。实验/生产来源只由 HMI 启动时向
Agent 查询的 `SourceInfo` 决定；部署环境变量不得伪造或覆盖数据来源。

## 发布布局

```text
/opt/block/releases/<version>/
├── bin/
│   ├── block-agent
│   ├── block-hmi
│   └── plc-simulator        # 仅 lab
├── deploy/
│   ├── health-check.sh
│   ├── install-users.sh
│   ├── install.sh
│   ├── verify-install.sh
│   ├── verify-static.sh
│   ├── rollback.sh
│   ├── config/
│   │   ├── block-agent.example.json
│   │   ├── block-agent-bdm.example.json
│   │   ├── block-agent-simulator-bdm.example.json
│   │   ├── block-agent-simulator.example.json
│   │   └── plc-simulator.example.json
│   ├── tests/
│   │   └── deploy-regression.sh
│   └── systemd/
└── manifest.txt

/opt/block/current -> /opt/block/releases/<version>
/var/lib/block-release/transactions/<transaction>/
```

`current` 用临时符号链接加 `mv -T` 原子切换。`manifest.txt` 记录版本、profile、Git/Common 基线、UTC 时间、上一版本、二进制 SHA-256、非秘密 Agent/Simulator 配置 SHA-256 和 HMI 证书指纹；不记录私钥。同一 release 的配置哈希不可变。每次变更前会记录旧 `current`、unit、非秘密配置、证书文件、受管父目录的数值 UID/GID/mode，以及服务启用/活动状态。

SQLite、Simulator 状态、日志、证书私钥和现场配置都不放进发布目录或 Git。
Agent SQLite 位于独立数据盘挂载点 `/var/lib/block/block.db`，回滚不会删除
或移动它。真实安装前必须由 `BLK-REL` 备份并确认目标分区内容，再按 UUID
把数据分区稳定挂载到 `/var/lib/block`，将挂载点设为
`block-agent:block-agent`、`0700`；安装器和 unit 都会拒绝把正式数据库
退回根分区。

## Fresh-host 安装

### 1. 发布前离线准备

在发布工作站构建目录中准备：

```text
artifact/
└── bin/
    ├── block-agent
    ├── block-hmi
    └── plc-simulator        # lab 才需要
```

准备以下主机本地文件：

- 非秘密 `block-agent.json`；
- lab 使用的非秘密 `plc-simulator.json`；
- 本机信任链 `ca.crt`；
- 包含访问名称或 `127.0.0.1` IP SAN 的 `block-hmi.crt`；
- 与证书匹配、源文件权限不高于 `0600` 的 `block-hmi.key`。

启用 BDM 时还需准备：

- 真实两机联调以 `block-agent-simulator-bdm.example.json` 为模板；它同时
  开启 PLC Simulator 和 BDM 上行，避免现场拼装配置。无 Simulator 的生产
  模式才使用 `block-agent-bdm.example.json`。当前实验端点固定写为
  `mqtts://192.168.1.105:8883`；
- Release 必须把样例中的 `softwareVersion` 精确替换为安装命令的
  `--version`，把 `architecture` 设为 `arm64`，并把 `hardwareModel`、
  `osVersion` 和 `streamGeneration` 替换为目标机的已核对事实；安装器拒绝
  未替换的占位值、版本不一致和非法 generation；
- 只用于验证 BDM Broker 服务端证书的 `bdm-server-ca.crt`；
- CN 精确等于配置中不透明 `blk-<32位小写十六进制>` principal、含
  `clientAuth` EKU 的 Block 客户端证书及匹配私钥。

服务端信任 CA 与 Block 客户端证书的签发 CA 可以分离。`--bdm-ca` 只安装
服务端信任根；Block 客户端链由 BDM Broker 配置的 client CA 在握手时验证，
不得误用服务端 CA 校验客户端证书。

真实 Wi-Fi 值、密码、令牌、真实 `.env` 和私钥不得进入仓库或构建产物。安装器会拒绝常见秘密字段名出现在 JSON 配置中，拒绝证书或 CA 文件包含任何私钥 PEM 块，并验证证书有效期、信任链和公私钥匹配。

源包中的下列脚本必须保持 `0755`：

```bash
test -x install-users.sh
test -x install.sh
test -x health-check.sh
test -x verify-install.sh
test -x rollback.sh
test -x verify-static.sh
test -x tests/deploy-regression.sh
```

### 2. 静态门禁

在开发机或构建机执行，不需要 root，不接触真实设备：

```bash
./verify-static.sh
```

该脚本执行所有 shell 的 `bash -n`、五个 JSON 样例解析，并检查 socket
路径、用户组矩阵、HMI `8s` 超时、TLS curl、生产/实验开关及 systemd
静态网络门禁。它还验证 BDM 服务端 CA 与客户端签发 CA 分离时可以安装，
并在工作区 `.cache` 沙箱内执行安装失败、父目录权限恢复、人工回滚、
fresh-host 清理、配置哈希不可变、证书/CA 私钥拒绝以及错误回滚事务拒绝
的反例测试。release 会同时保存 `verify-static.sh` 所依赖的 `config/`
样例和 `tests/deploy-regression.sh`，回归测试会从打包后的 release 再执行
一次静态入口，避免只复制入口脚本而遗漏依赖。

### 3. 生产安装

仅由 `BLK-REL` 在主机已经记录服务、端口、配置摘要和数据备份位置后执行：

```bash
cd /path/to/reviewed/deploy/block
sudo env BLOCK_RELEASE_ROLE=BLK-REL ./install.sh \
  --execute \
  --profile production \
  --version 0.1.0 \
  --artifact-dir /path/to/artifact \
  --agent-config /secure/staging/block-agent.json \
  --tls-cert /secure/staging/block-hmi.crt \
  --tls-key /secure/staging/block-hmi.key \
  --tls-ca /secure/staging/ca.crt \
  --git-commit <full-block-commit> \
  --common-baseline <full-common-commit>
```

### 4. 实验安装

```bash
sudo env BLOCK_RELEASE_ROLE=BLK-REL ./install.sh \
  --execute \
  --profile lab \
  --version 0.1.0-lab.1 \
  --artifact-dir /path/to/artifact \
  --agent-config /secure/staging/block-agent-simulator.json \
  --simulator-config /secure/staging/plc-simulator.json \
  --tls-cert /secure/staging/block-hmi.crt \
  --tls-key /secure/staging/block-hmi.key \
  --tls-ca /secure/staging/ca.crt \
  --git-commit <full-block-commit> \
  --common-baseline <full-common-commit> \
  --control-admin <explicit-lab-operator>
```

### 5. 启用 BDM 上行

当前两机实验环境中，Block 使用 Wi-Fi 所在网络主动连接
`192.168.1.105:8883`。除使用 BDM 版 Agent 配置外，安装命令增加：

```bash
  --agent-config /secure/staging/block-agent-simulator-bdm.json \
  --bdm-ca /secure/staging/bdm-server-ca.crt \
  --bdm-client-cert /secure/staging/block-mqtt-client.crt \
  --bdm-client-key /secure/staging/block-mqtt-client.key
```

三个 BDM 证书参数必须与 `bdm.enabled=true` 同时出现。以后专用路由器固定
BDM 地址时，只修改受审查的部署配置、Agent unit 的出站白名单和 BDM
服务端证书 SAN；不得修改 `siteId/blockId/deviceId` 或 principal 来迁就 IP。

管理员组变更仍由 `BLK-REL` 显式完成：

```bash
sudo usermod -aG block-sim-control <explicit-lab-operator>
```

随后重新登录该管理员会话。`--control-admin` 不自动加组，只把已加组人员加入本次验证白名单；任何没有在命令中列出的 control 组成员都会使验证失败。

安装器具有以下幂等边界：

- 同一版本、profile、基线和二进制必须完全相同，否则拒绝覆盖；
- 同一 release 的 Agent/Simulator 配置 SHA-256 必须与 manifest 完全相同；
- 当主机已经与该版本及配置一致时，只收敛 enable/start 状态并重新验证，不创建新的安装事务；
- 无论复用既有 release 还是执行正常安装，都会先启动 Agent，并通过有界重试确认
  `/run/block-agent/api/block-agent.sock` 的 `/healthz` 已就绪后才启动 HMI；
  约 30 秒内仍未就绪会输出 unit 状态和 socket 路径并失败，不使用固定 `sleep`
  猜测启动时序；
- 配置或 unit 漂移会先备份再恢复到发布内容；
- 半成品或同版本不同内容不会被静默覆盖，必须由 `BLK-REL` 调查。
- 从 lab 切换 production 会删除 Simulator 配置，并验证 Simulator 二进制、
  服务和配置均不再处于生产运行面。

安装事务在修改主机前保存可恢复快照。配置写入、unit 安装、`current`
切换、服务重启或安装后验证任一步失败，安装器都会停止本次服务并恢复
旧 unit、配置、证书、受管父目录 UID/GID/mode、链接、事务指针及服务状态。
目录状态会先完整校验六个受管路径，再统一恢复；恢复文件时不会重新标记已有父目录，
并拒绝父目录 symlink。Fresh host 没有旧 release
时也会删除本次写入的受管主机文件和 `current`，同时保留失败事务目录供
审计；已创建的锁定服务账户和完整、未激活的 release 不自动删除。

## 主机验证

安装器会自动调用：

```bash
sudo /opt/block/current/deploy/verify-install.sh \
  --profile production \
  --ca /etc/block/certs/ca.crt \
  --hmi-url https://127.0.0.1:8443/healthz
```

lab 验证增加 `--profile lab` 和每个 `--control-admin USER`。验证内容包括：

- 使用指定 CA、主机名/IP 和 TLS 1.2+ 请求 HMI，没有跳过证书校验；
- Agent UDS 健康；lab 时 Simulator I/O/control UDS 存在；
- 服务用户的精确组成员关系；
- 配置、证书、状态目录和 socket 的 owner/group/mode；
- 使用 `runuser` 实测 HMI、Agent、Simulator 的 Linux 允许/拒绝矩阵；
- unit 与当前 release 一致且没有未审查 drop-in；
- Agent 无业务入站，只允许当前 BDM 的 MQTTS 出站；Simulator 无网络，
  HMI 只允许 loopback 的静态沙箱；
- `8443` 只绑定 loopback；
- TCP/UDP `80`、`1883`、`8080`、`8081` 都无监听，因此也不存在明文跳转；
- production 的 Simulator 不 enabled、不 active；lab 则相反。

在 Ubuntu 18.04 主机上另执行 systemd 237 解析验证：

```bash
sudo systemd-analyze verify \
  /etc/systemd/system/block-agent.service \
  /etc/systemd/system/block-hmi.service \
  /etc/systemd/system/block-plc-simulator.service
```

若项目中以后出现第二份 HMI unit 镜像，发布门禁必须逐字比较它与本目录的 `systemd/block-hmi.service`；在完成该比较前，本目录是部署权威副本。

### TLS 负向门禁

以下测试必须表现为“连接失败”，不得通过任何跳过校验选项规避：

- 使用非签发 CA；
- 使用错误主机名访问；
- 使用过期证书；
- 把客户端最高 TLS 版本限制在 1.1；
- 尝试访问 `http://127.0.0.1:80`、`:8080` 或 `:8081`；
- 尝试明文 MQTT `127.0.0.1:1883`。

正向用例必须使用受信 CA 和证书覆盖的访问名称。证书负向用例由 `BLK-REL` 在隔离验证环境执行，不替换生产私钥。

## 无 Wi-Fi、无 BDM 验证

只在具有本地控制台和恢复手段的隔离实验主机执行，不能在 SSH 是唯一入口时直接关闭网卡：

1. 记录 `ss -lntup`、`ss -lx`、三个 unit 状态和日志起点。
2. 用实验网络策略阻断所有非 loopback 入站/出站。
3. 验证 Simulator（lab）、Agent、HMI 正常启动；HMI 状态、告警、历史和允许的现场操作正常。
4. `bdm.enabled=false` 时验证没有 BDM/MQTT 外部连接尝试；启用时允许
   Agent 后台重连 `192.168.1.105:8883`，但该失败不得影响本地功能。
5. 断开 Simulator 后，Agent/HMI 继续存活，数据转为 stale 且写操作禁用；恢复后重新采样。
6. 重启 Agent，验证 SQLite 中最后状态、历史、审计和命令幂等记录恢复。
7. 恢复实验网络策略，再次执行 `verify-install.sh`。

运行中 SQLite 的备份必须使用 SQLite 在线备份机制，或停止 Agent 后保存一致的数据库/WAL 状态；不能只复制主文件后宣称成功。

## 回滚

先确认 `/var/lib/block-release/current-transaction` 指向本次安装事务，并保留现场数据：

```bash
sudo env BLOCK_RELEASE_ROLE=BLK-REL \
  /opt/block/current/deploy/rollback.sh \
  --execute \
  --ca /etc/block/certs/ca.crt \
  --hmi-url https://127.0.0.1:8443/healthz
```

回滚流程：

1. 取得发布锁并验证事务路径；
2. 停止 HMI、Agent、Simulator；
3. 恢复上一份 unit、非秘密配置、已记录的受管父目录元数据和 release profile 记录；
4. 原子切回上一 `current`；
5. `daemon-reload`，恢复原 enable/active 状态；
6. 若上一 Agent 原本 active，则等待其 UDS `/healthz` 就绪；
7. 仅在 Agent 就绪后启动原本 active 的 HMI，再执行受信 TLS 和 UDS 健康检查；
8. 恢复上一事务指针并记录 UTC 回滚时间。

人工回滚前会强制验证 `current-transaction` 记录的 release 目标和 manifest
SHA-256 与当前 release 完全一致，拒绝陈旧、篡改或串线事务；因此同一不可变
release 的幂等重新应用也具有独立、可验证的事务。人工回滚不会删除 SQLite、
Simulator 状态、日志、证书或任何 release。Fresh install 没有上一
`current` 时会明确拒绝“伪回滚”；安装过程自身的失败由安装器内置恢复
处理。如果回滚健康检查失败，保持现场和事务记录，交由 `BLK-REL` 诊断，
不自动删除数据。
