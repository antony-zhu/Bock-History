# Block v2 真机发布与回滚

本文件是 Block 的唯一正式真机发布流程。每次安装（包括首次迁移）必须从受控暂存的
候选制品执行 `artifact/deploy/install.sh`；绝不调用 `/opt/block/current/deploy/install.sh`
升级。安装成功后，该候选的同一份流程会随 release 复制到 `current/deploy/`，仅用于
验收或后续手工回滚。不要另建手工发布流程、第二个 systemd 服务或临时发布工具。

本流程只发布 Block runtime 和本地 HMI，不配置 Wi-Fi、BDM、PLC 点位或 PLC
控制逻辑。整个发布过程不得向 PLC 写入任何值。

## 1. 发布边界与身份

发布单必须先确认目标的 `siteId`、`blockId` 和 `deviceId`。IP 地址和主机名只
用于连接，不能替代这三个业务身份。

| 项目 | 正确用途 |
| --- | --- |
| `block.service` | 唯一 Block runtime；仅在 `127.0.0.1:8444` 以 TLS 提供本地 HMI、`/healthz`、API 和 WSS，在 `0.0.0.0:8443` 提供独立维护 HTTPS。 |
| `block-kiosk.service` | 等待严格的本机 HTTPS 健康检查后，使用 Chromium 打开 `https://127.0.0.1:8444/`。8444 仅限本机回环访问，绝不对外开放；8080/8081 不监听、不重定向。 |
| `ssh-bootstrapd` | 独立的 HTTPS 管理服务；如已安装，它监听 9443，不改变 Block runtime、HMI 或 PLC 的职责。 |
| 系统 SSH | 22/tcp 仅为调试/设备管理例外，不能作为业务通信通道。 |

`/etc/block/block.env` 必须为本机业务同时提供 `BLOCK_LOCAL_HTTPS_ADDRESS`
（固定为 `127.0.0.1:8444`）、`BLOCK_LOCAL_TLS_CERT`、`BLOCK_LOCAL_TLS_KEY` 和
`BLOCK_LOCAL_TLS_CA`。当前设备候选使用本机 leaf
`/etc/block/certs/block-hmi.crt` 与私钥
`/etc/block/certs/block-hmi.key`；公开 CA 为
`/usr/local/share/ca-certificates/block-dmp-blk-rel-001.crt`，单独供 kiosk 和
健康检查校验，不能把私钥写入环境文件。该公开 CA 必须可由 `block` 和 `block-ui`
读取；不要让 kiosk 使用不可读的 `/etc/block/certs/ca.crt`。证书必须包含
`127.0.0.1` 的 SAN，业务 TLS 最低为 1.2。证书、私钥或 CA 缺失、不可读、无效或
不匹配均为安装失败，不能回退到明文。

短期 HTTPS 发布证书只会映射到非 root 的发布/调试账户。它不能用于 root 安装，
也不能以 sudo 绕过此限制。真机安装只能由设备管理员使用**已批准的固定安装
身份**完成；该身份的私钥、路径、内容、指纹和连接参数均不得写入仓库、发布
报告或本文件。固定安装身份只用于管理员操作，不得被 Block 业务程序使用。

## 2. 本地构建、测试和制品哈希

在任何本地构建或测试前，把所有临时目录放在工作区 `.cache`。以下变量须在
同一个 shell 中设置；`WORKSPACE` 和 `BLOCK_REPO` 必须是绝对路径。

```bash
WORKSPACE=/absolute/path/to/Block-DMP
BLOCK_REPO="$WORKSPACE/worktrees/Block/<release-worktree>"
VERSION=<approved-version>
CACHE_ROOT="$WORKSPACE/.cache/block-release-$VERSION"
ARTIFACT_DIR="$CACHE_ROOT/artifact"

mkdir -p "$CACHE_ROOT/tmp" "$CACHE_ROOT/go-tmp"
export TEMP="$CACHE_ROOT/tmp"
export TMP="$CACHE_ROOT/tmp"
export TMPDIR="$CACHE_ROOT/tmp"
export GOTMPDIR="$CACHE_ROOT/go-tmp"
```

运行仓库已有的发布回归检查并生成不可变制品。`build.sh` 要求制品目录尚不存在。

```bash
cd "$BLOCK_REPO"
deploy/block/tests/deploy-regression.sh
deploy/block/build.sh --output "$ARTIFACT_DIR" --version "$VERSION"

(
  cd "$ARTIFACT_DIR"
  find . -type f -print0 | sort -z | xargs -0 sha256sum
) > "$CACHE_ROOT/artifact.sha256"
```

制品必须包含下列文件，且 `VERSION` 与发布单相同：

```text
bin/block-agent
web/index.html
web/assets/points.json
deploy/install.sh、rollback.sh、health-check.sh、systemd unit、配置示例及其引用的 helper/test
VERSION
```

`artifact.sha256` 是传输前后的比对依据。它只覆盖发布制品；不要对私钥、密码、
真实配置或 Wi-Fi 文件计算、记录或上传哈希。

## 3. 真机只读盘点与备份

只有设备管理员在已批准的固定安装身份下才能进入本节。先创建一个发布记录，
随后所有命令均在目标机执行。先做只读盘点；身份、当前版本或服务状态有任何一
项与发布单不符时立即 STOP，不传输、不安装、不重启。

在目标机的同一个 shell 中重新设置发布版本。此值必须与第 2 节已构建并签核的
`VERSION` 完全一致，后续备份、暂存、安装和验收命令都使用它。

```bash
VERSION='<approved-version>'
case "$VERSION" in
  ''|*[!A-Za-z0-9._-]*)
    printf 'invalid approved version: %s\n' "$VERSION" >&2
    exit 1
    ;;
esac

sudo /opt/block/current/deploy/version.sh
sudo systemctl is-active block.service block-kiosk.service
sudo ss -ltnp | grep -E ':(22|8443|8444|9443)([[:space:]]|$)'
! sudo ss -ltnp | grep -Eq ':(8080|8081)([[:space:]]|$)'
sudo awk -F= \
  '/^BLOCK_MQTTS_V2_(SITE_ID|BLOCK_ID|DEVICE_ID)=/ { print }' \
  /etc/block/block.env
sudo awk -F= \
  '$1 == "BLOCK_MQTTS_V2_ENABLED" { print }' /etc/block/block.env
```

盘点记录必须包含：当前 `current` 指向和版本、两个 Block service 的状态、监听
端口、`siteId`/`blockId`/`deviceId`、BDM 启用状态、PLC endpoint 是否已配置，
以及 Wi-Fi 配置文件是否存在。只记录 Wi-Fi 文件的存在状态，绝不读取或输出其
内容。PLC endpoint 与点位在本步骤只读，不得扫描写入或下发控制。

在任何设备写入前，创建 root 独占的备份目录：

```bash
STAMP=$(date -u +%Y%m%dT%H%M%SZ)
BACKUP="/var/backups/block/pre-$VERSION-$STAMP"
sudo install -d -o root -g root -m 0700 "$BACKUP"
sudo stat -c '%U:%G:%a %n' "$BACKUP"
```

备份至少包含下列内容，并在发布记录中写明每一项的源路径和备份路径：

| 备份对象 | 要求 |
| --- | --- |
| 当前 release | 记录 `/opt/block/current` 的链接值、解析后的 release 路径、`/var/lib/block-release/current-version` 与 `previous-release`。 |
| 配置与 unit | 备份 `/etc/block/block.env`、`block.service` 和 `block-kiosk.service`；配置文件不得复制到工作区或报告。 |
| SQLite | 使用 SQLite 的一致性备份，并对备份执行 `PRAGMA integrity_check`。SQLite 备份不以普通运行中文件复制替代。 |
| Chromium profile、cache 与 NSS 信任库 | 分别备份 `/home/block-ui/.config/chromium`、`/home/block-ui/.cache/chromium` 和（如存在）`/home/block-ui/.pki/nssdb`。必须复制符号链接本身，不能解引用。 |
| 现场状态 | 记录 BDM/Wi-Fi 文件存在性与 PLC endpoint 状态，不复制 Wi-Fi 内容，也不写 PLC。 |

Chromium 目录中可能有悬空 `Singleton*` 符号链接。使用 `cp -a --no-dereference`
（或等价的保留符号链接方式）备份，并用 `find -P` 检查；不得用会解引用链接的
递归 diff 作为备份有效性判断。例如：

```bash
sudo cp -a --no-dereference -- \
  /home/block-ui/.config/chromium "$BACKUP/chromium-config"
sudo cp -a --no-dereference -- \
  /home/block-ui/.cache/chromium "$BACKUP/chromium-cache"
if sudo test -e /home/block-ui/.pki/nssdb; then
  sudo cp -a --no-dereference -- \
    /home/block-ui/.pki/nssdb "$BACKUP/block-ui-nssdb"
fi
sudo find -P "$BACKUP" -type l -ls
```

备份未完整、权限不是 `root:root 0700`、SQLite 检查失败或 Chromium profile 复制
失败时立即 STOP。不得以删除 profile、重建账户或跳过备份继续发布。

## 4. 制品暂存与安装

通过已批准的设备管理通道，把发布制品和一份受保护的配置副本暂存到目标机。
暂存目录同样必须 root 独占；它不是 release 目录，也不是长期配置位置。

```bash
STAGE="/var/backups/block/stage-$VERSION-$STAMP"
sudo install -d -o root -g root -m 0700 "$STAGE"

# 通过批准的管理通道把 artifact/ 与 artifact.sha256 传到 $STAGE；
# 复制当前受保护配置以准备 $STAGE/block.env，不在终端或报告中显示其内容。
sudo install -m 0640 -o root -g block /etc/block/block.env "$STAGE/block.env"
sudo sh -c "cd '$STAGE/artifact' && sha256sum -c ../artifact.sha256"
sudo test -x "$STAGE/artifact/bin/block-agent"
sudo test -f "$STAGE/artifact/web/index.html"
sudo test -f "$STAGE/artifact/web/assets/points.json"
sudo test -x "$STAGE/artifact/deploy/install.sh"
sudo test -x "$STAGE/artifact/deploy/rollback.sh"
sudo test -x "$STAGE/artifact/deploy/health-check.sh"
sudo test -f "$STAGE/artifact/deploy/systemd/block.service"
sudo test -f "$STAGE/artifact/deploy/systemd/block-kiosk.service"
sudo test -f "$STAGE/artifact/deploy/config/block.env.example"
test "$(sudo cat "$STAGE/artifact/VERSION")" = "$VERSION"
```

`block.env` 不能持久化点位，且不得包含 PLC 点位表、密码、私钥内容或 Wi-Fi
设置。制品哈希、布局或版本任何一项不符时立即 STOP；不要覆盖原 release，也
不要手工修改当前链接。

直接使用暂存候选制品内的安装器执行一次正式安装；这也是首次迁移入口：

```bash
sudo "$STAGE/artifact/deploy/install.sh" --execute \
  --artifact-dir "$STAGE/artifact" \
  --config "$STAGE/block.env" \
  --version "$VERSION"
```

安装器的固定顺序是：复制并校验 release、原子切换 `current`、`systemctl
daemon-reload`、enable 两个 service、**明确 `systemctl restart block.service`**、
通过本地健康检查后再 `systemctl restart block-kiosk.service`。不要用 `enable --now`
代替这个顺序，也不要在安装器运行中手工重启任何服务。

在 `current` 已切换后的失败会触发安装器的自动回滚。若安装器返回失败，先检查
当前版本和 service 状态；不要立刻再次运行默认 rollback，也不要重复安装。自动
回滚已经执行过时，再执行一次默认 rollback 可能会切换到错误的方向。

安装器会在写入配置、unit 或 `current` 链接之前严格验证本机证书、私钥和公开 CA，
然后创建安装前快照。切换后的任何失败都会用该快照恢复 `block.service`、
`block-kiosk.service`、`/etc/block/block.env`、`current`/`previous-release` 状态，
并以恢复目标自身的健康检查脚本重新启动服务。

## 5. 发布验收

安装器成功返回后，先验证版本、服务和本地健康检查：

```bash
sudo /opt/block/current/deploy/verify-install.sh --expected-version "$VERSION"
sudo /opt/block/current/deploy/version.sh
sudo systemctl is-active block.service block-kiosk.service
```

### HMI 静态资源与缓存规则

HMI 静态资源必须返回 `Cache-Control: no-store`。保留原始响应头文件，再从冒号
后的 header value 去掉首尾空白后比较；不能把整行 `Cache-Control: no-store`
直接和 `no-store` 比较。

```bash
HMI=https://127.0.0.1:8444
HMI_CA=/usr/local/share/ca-certificates/block-dmp-blk-rel-001.crt
ROOT_HEADERS="$BACKUP/after-root.headers"

ROOT_STATUS=$(curl --proto '=https' --tlsv1.2 --cacert "$HMI_CA" --fail --silent --show-error --dump-header "$ROOT_HEADERS" \
  --output /dev/null --write-out '%{http_code}' "$HMI/")
test "$ROOT_STATUS" = "200"

CACHE_CONTROL=$(awk '
  /^[^:]+:/ {
    key = $0
    sub(/:.*/, "", key)
    if (tolower(key) == "cache-control") {
      value = $0
      sub(/^[^:]*:[[:space:]]*/, "", value)
      sub(/[[:space:]]+$/, "", value)
      print value
    }
  }
' "$ROOT_HEADERS")
test "$CACHE_CONTROL" = "no-store"
```

验收规则如下：

- `GET /` 必须是 `200`，并且 `Cache-Control` 的解析值为 `no-store`。
- `GET /assets/hmi.mjs` 也必须是 `200` 且为 `no-store`。
- `GET /index.html` 由 Go `http.FileServer` 返回规范化 `301` 和 `Location: ./`
  是允许的；必须跟随该跳转并最终验证根路径 `/` 为 `200`。不得把 `/index.html`
  的直接 `301` 误判为发布失败，也不得要求它直接返回 `200`。

例如，保留 `/index.html` 的原始头后再跟随跳转：

```bash
curl --proto '=https' --tlsv1.2 --cacert "$HMI_CA" --silent --show-error --dump-header "$BACKUP/after-index.headers" \
  --output /dev/null "$HMI/index.html"
curl --proto '=https' --tlsv1.2 --cacert "$HMI_CA" --fail --location --silent --show-error \
  --dump-header "$BACKUP/after-index-follow.headers" \
  --output /dev/null "$HMI/index.html"
HMI_MODULE_STATUS=$(curl --proto '=https' --tlsv1.2 --cacert "$HMI_CA" --fail --silent --show-error \
  --dump-header "$BACKUP/after-hmi-module.headers" \
  --output /dev/null --write-out '%{http_code}' "$HMI/assets/hmi.mjs")
test "$HMI_MODULE_STATUS" = "200"
```

### Kiosk 与真实屏幕

HTTPS 响应、制品哈希和静态文件哈希一致只能证明服务端资源正确，**不能**证明
设备屏幕已经刷新。必须在真实 X11 屏幕上验收当前 HMI。

1. 等待 Chromium 启动完成，连续两次采集可见的全屏 X11 窗口、其 Browser PID
   和进程命令行中的 `https://127.0.0.1:8444/`；两次之间至少间隔 5 秒。
2. 两次采集的 PID、URL 与全屏窗口必须一致。`block-kiosk.service` 的 wrapper
   或 MainPID 在启动过程中可能切换，不能单独用它作为成功依据。
3. 人工核对真实屏幕显示的是本次 HMI 页面，并保存非白屏的实机屏幕截图到
   受保护的发布证据目录。白色过渡帧不是验收结果。

可以使用现有 X11 工具采集窗口和 PID，例如 `wmctrl -lpG` 配合 Chromium 进程
命令行；具体工具不可用时，先 STOP 并改用设备已有的屏幕采集方式，不要绕过
真实屏幕验收。

若真实屏幕仍显示旧页面，先确认本节的 HMI HTTPS 验收已通过，再执行以下唯一的
缓存修复步骤。只有在第 3 节的 profile/cache 备份已成功后才允许操作：

```bash
sudo systemctl stop block-kiosk.service
sudo rm -rf -- \
  /home/block-ui/.cache/chromium/Default/Cache \
  '/home/block-ui/.cache/chromium/Default/Code Cache'
sudo systemctl start block-kiosk.service
```

此步骤只能清空上面两个固定目录。禁止修改
`/home/block-ui/.config/chromium`、Local Storage、IndexedDB、账号、PLC、
SQLite、BDM 或 Wi-Fi。随后重新执行本节的 HTTPS、稳定 PID/URL 和真实屏幕验收。

### Chromium 对本机 CA 的受控信任（仅必要时）

先执行上面的严格健康检查和 HMI HTTPS 验收；它们必须使用
`/usr/local/share/ca-certificates/block-dmp-blk-rel-001.crt`，不得使用 `-k`、`--insecure` 或
`--ignore-certificate-errors`。只有这些严格检查已经通过、而 Chromium 仅因
本机 CA 不受信任无法打开 kiosk 时，才允许设备管理员进行以下一次受控导入：

```bash
sudo systemctl stop block-kiosk.service
sudo test -d /home/block-ui/.pki/nssdb
sudo cp -a --no-dereference -- \
  /home/block-ui/.pki/nssdb "$BACKUP/block-ui-nssdb-before-local-ca"
sudo -u block-ui certutil -d sql:/home/block-ui/.pki/nssdb \
  -A -n block-local-business-ca -t 'C,,' \
  -i /usr/local/share/ca-certificates/block-dmp-blk-rel-001.crt
sudo systemctl start block-kiosk.service
```

这不是安装器或 kiosk unit 的自动步骤。`certutil` 不存在、NSS 数据库缺失、导入
失败或浏览器仍出现证书错误时立即 STOP；不要改用忽略证书错误的 Chromium 参数。
该步骤只导入公开 CA，绝不导入私钥。若需撤销，应由设备管理员在已备份的 profile
范围内恢复 `block-ui-nssdb-before-local-ca`，然后重新执行严格 HTTPS 验收。

## 6. 失败、回滚与 STOP 条件

下列任一情况都必须 STOP 并保留证据：目标身份不匹配、制品哈希不匹配、备份不
完整、安装器失败、健康检查失败、根路径不是 `200`、`no-store` 缺失、X11 PID/URL
不稳定、屏幕仍非本次页面，或没有可用的真实屏幕采集方式。

安装器在切换后失败时会自行调用回滚。手工回滚只适用于：安装器已成功返回、
当前版本仍是目标版本，并且已确认 `/var/lib/block-release/previous-release` 指向
安装前的 release。满足这三个条件后，执行：

```bash
sudo /opt/block/current/deploy/rollback.sh --execute
sudo /opt/block/current/deploy/verify-install.sh
sudo systemctl is-active block.service block-kiosk.service
```

回滚会恢复安装前记录的不可变 release 和对应的 `block.service`、
`block-kiosk.service`、本机 TLS 配置与 `current`/`previous-release` 状态；健康检查
使用恢复目标 release 自带的兼容参数。它不恢复 `/var/lib/block` SQLite 数据。回滚
成功后仍要重新验证健康检查、HMI HTTPS 规则、稳定 X11 PID/URL 与真实屏幕。

若正式回滚本身失败，才使用第 3 节的备份进行设备管理员控制下的恢复：先停止
kiosk 和 Block，再恢复记录的 release 链接、release state、配置和 unit；只有
SQLite 已确认损坏时才恢复 SQLite 备份。随后执行 `daemon-reload`，先启动并验收
Block，再启动并验收 kiosk。不得删除 release 目录、profile 或现场数据来伪造恢复。

不要自动重试或连续重复安装。任何新的尝试都必须先说明故障原因、确认当前版本
和备份可用，并取得新的设备写入授权。

## 7. 发布报告与清理

完成、回滚或 STOP 后都要产生一份不含秘密的发布报告。报告至少包含：

- 发布单号、`siteId`、`blockId`、`deviceId`；
- 目标版本、提交、构建时间、制品哈希和暂存校验结果；
- 发布前后 current release、service/port/health 状态；
- 备份目录、暂存目录、SQLite 检查、回滚结果；
- 原始 HTTPS 头文件位置、`/` 的 200/no-store 验收结果；
- 两次 X11 PID/URL 采集和最终实机屏幕截图位置；
- 全程未向 PLC 写入的确认。

报告不得包含私钥、密码、真实配置内容、Wi-Fi 内容、证书私钥、固定安装身份的
路径/内容/指纹，或可复用的 SSH 会话材料。

验收结束后，删除本次运行时创建的 SSH 私钥副本、known_hosts、临时 SSH 配置和
会话目录；不要删除设备管理员保存的原始固定身份。保留有效备份、暂存证据和
release 目录，直到设备管理员按留存策略确认可以清理。
