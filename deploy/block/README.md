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

### Windows 工作区的推荐打包方式

Windows 的部分 `tar` 路径会丢失 `deploy/tests/*.sh` 的执行位。必须在 WSL/Linux
shell 中打包，而不是用资源管理器或 Windows `tar.exe` 重新封装；上传同一个压缩包和
manifest，设备解包后先检查模式。执行位不符时立即 STOP 并从原始候选重新打包，不能
在设备上猜测性 `chmod` 后继续安装。

```bash
# 在 WSL/Linux shell，且在生成 artifact.sha256 后执行。
cd "$CACHE_ROOT"
tar --format=posix -czf artifact.tar.gz artifact artifact.sha256
tar -tzf artifact.tar.gz >/dev/null
```

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
sudo readlink -f /opt/block/current
sudo cat /var/lib/block-release/current-version
sudo cat /var/lib/block-release/previous-release
for SERVICE in block.service block-kiosk.service ssh-bootstrapd.service; do
  printf '%s=' "$SERVICE"
  sudo systemctl is-active "$SERVICE" || true
done
sudo ss -ltnp | grep -E ':(22|8443|8444|9443)([[:space:]]|$)'
! sudo ss -ltnp | grep -Eq ':(8080|8081)([[:space:]]|$)'
sudo awk -F= \
  '/^BLOCK_MQTTS_V2_(SITE_ID|BLOCK_ID|DEVICE_ID)=/ { print }' \
  /etc/block/block.env
sudo awk -F= \
  '$1 == "BLOCK_MQTTS_V2_ENABLED" { print }' /etc/block/block.env
sudo awk -F= \
  '$1 ~ /^BLOCK_(LOCAL_HTTPS_ADDRESS|MAINTENANCE_HTTPS_ADDRESS|MQTTS_V2_ENDPOINT)$/ { print }' \
  /etc/block/block.env
```

盘点记录必须包含：当前 `current` 与 `previous-release`、当前版本、三个 service
（`block.service`、`block-kiosk.service`、`ssh-bootstrapd.service`）的状态、监听端口、
安全的配置摘要（地址、BDM 开关和业务身份，不含秘密）、`siteId`/`blockId`/
`deviceId`、PLC endpoint 是否已配置，以及 Wi-Fi 配置文件是否存在。只记录 Wi-Fi
文件的存在状态，绝不读取或输出其内容。PLC endpoint 与点位在本步骤只读，不得
扫描写入或下发控制。

在任何设备写入前，创建 root 独占的备份目录：

```bash
STAMP=$(date -u +%Y%m%dT%H%M%SZ)
BACKUP="/var/backups/block/pre-$VERSION-$STAMP"
sudo install -d -o root -g root -m 0700 "$BACKUP"
sudo stat -c '%U:%G:%a %n' "$BACKUP"

# 保存 current/previous、两个 unit、受保护配置和维护端口基线；不显示配置内容。
sudo cp -a --no-dereference /opt/block/current "$BACKUP/current-link"
sudo cp -a -- "$(sudo readlink -f /opt/block/current)" "$BACKUP/current-release"
sudo cp -a -- /var/lib/block-release/current-version \
  /var/lib/block-release/previous-release "$BACKUP/"
sudo cp -a -- /etc/systemd/system/block.service \
  /etc/systemd/system/block-kiosk.service /etc/block/block.env "$BACKUP/"
for SERVICE in block.service block-kiosk.service ssh-bootstrapd.service; do
  sudo systemctl is-active "$SERVICE" | sudo tee "$BACKUP/$SERVICE.before" >/dev/null || true
done
sudo ss -ltnH | awk '$4 ~ /:(8443|9443)$/ { print $1, $4 }' | sort | \
  sudo tee "$BACKUP/maintenance-listeners.before" >/dev/null
```

备份至少包含下列内容，并在发布记录中写明每一项的源路径和备份路径：

| 备份对象 | 要求 |
| --- | --- |
| 当前 release | 记录 `/opt/block/current` 的链接值、解析后的 release 路径、`/var/lib/block-release/current-version` 与 `previous-release`。 |
| 配置与 unit | 备份 `/etc/block/block.env`、`block.service` 和 `block-kiosk.service`；配置文件不得复制到工作区或报告。 |
| SQLite | 使用 SQLite 的一致性备份，并对备份执行 `PRAGMA integrity_check`。SQLite 备份不以普通运行中文件复制替代。 |
| Chromium profile、cache 与 NSS 信任库 | 分别备份 `/home/block-ui/.config/chromium`、`/home/block-ui/.cache/chromium` 和（如存在）`/home/block-ui/.pki/nssdb`。必须复制符号链接本身，不能解引用。 |
| 现场状态 | 记录 BDM/Wi-Fi 文件存在性与 PLC endpoint 状态，不复制 Wi-Fi 内容，也不写 PLC。 |

设备自带 Python 3.6，不能假定存在 `sqlite3.Connection.backup`。本次和后续发布均
使用短暂停机冷备：先停止 kiosk、再停止 Block，复制 SQLite，运行完整性检查，随后
**先恢复旧 Block、再恢复旧 kiosk**，确认恢复后才可进入制品暂存。不要为了备份安装
Python 包或其他工具。

```bash
DB=/var/lib/block/block.db
sudo systemctl stop block-kiosk.service
sudo systemctl stop block.service
if ! sudo cp -a -- "$DB" "$BACKUP/block.db" || \
   ! sudo sqlite3 "$BACKUP/block.db" 'PRAGMA integrity_check;' | grep -Fxq ok; then
  sudo systemctl start block.service
  sudo systemctl start block-kiosk.service
  exit 1
fi
sudo sqlite3 -noheader "$BACKUP/block.db" \
  'SELECT username || "|" || role FROM local_accounts ORDER BY username; SELECT "idle=" || idle_timeout_seconds FROM local_system_settings WHERE singleton_id = 1;' | \
  sudo tee "$BACKUP/auth-before.txt" >/dev/null
sudo systemctl start block.service
sudo systemctl is-active --quiet block.service
sudo systemctl start block-kiosk.service
sudo systemctl is-active --quiet block-kiosk.service
```

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

# 通过批准的管理通道把 WSL/Linux 生成的 artifact.tar.gz 传到 $STAGE 后立即解包。
sudo tar -xzf "$STAGE/artifact.tar.gz" -C "$STAGE" --no-same-owner
# 复制当前受保护配置以准备 $STAGE/block.env，不在终端或报告中显示其内容。
sudo install -m 0640 -o root -g block /etc/block/block.env "$STAGE/block.env"
sudo sh -c "cd '$STAGE/artifact' && sha256sum -c ../artifact.sha256"
sudo file "$STAGE/artifact/bin/block-agent" | grep -Eq 'ELF .*ARM aarch64'
sudo test -x "$STAGE/artifact/bin/block-agent"
sudo test -f "$STAGE/artifact/web/index.html"
sudo test -f "$STAGE/artifact/web/assets/points.json"
sudo test -x "$STAGE/artifact/deploy/install.sh"
sudo test -x "$STAGE/artifact/deploy/rollback.sh"
sudo test -x "$STAGE/artifact/deploy/health-check.sh"
sudo test -f "$STAGE/artifact/deploy/systemd/block.service"
sudo test -f "$STAGE/artifact/deploy/systemd/block-kiosk.service"
sudo test -f "$STAGE/artifact/deploy/config/block.env.example"
for SCRIPT in \
  deploy/install.sh deploy/rollback.sh deploy/health-check.sh \
  deploy/install-users.sh deploy/version.sh deploy/verify-install.sh \
  deploy/verify-static.sh deploy/tests/deploy-regression.sh \
  deploy/tests/install-rollback-regression.sh; do
  sudo test -x "$STAGE/artifact/$SCRIPT"
done
sudo stat -c '%a %n' "$STAGE/artifact/deploy/tests/"*.sh
test "$(sudo cat "$STAGE/artifact/VERSION")" = "$VERSION"
```

`block.env` 不能持久化点位，且不得包含 PLC 点位表、密码、私钥内容或 Wi-Fi
设置。制品哈希、ELF 架构、执行位、布局或版本任何一项不符时立即 STOP；不要覆盖
原 release，也不要手工修改当前链接或在设备上修补候选的模式。

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

候选制品、候选 `deploy/`、本机 TLS 材料、配置和两个 unit 任一预检失败时，安装器
不得创建 release、写配置、切换 `current` 或停止现有服务。通过预检后才创建事务
snapshot；切换后的失败由候选自带的 rollback 恢复 snapshot，而不是调用旧
`current` installer。

## 5. 发布验收

安装器成功返回后，先验证版本、服务和本地健康检查：

```bash
sudo /opt/block/current/deploy/verify-install.sh --expected-version "$VERSION"
sudo /opt/block/current/deploy/version.sh
sudo systemctl is-active block.service block-kiosk.service
```

`verify-install.sh` 只把真实点位配置键（如 `POINTS_FILE`、`BLOCK_POINT_MAP`）视为
禁止项；它必须放行合法的 `BLOCK_MQTTS_V2_ENDPOINT`，不得按任意 `POINT` 子串误报。

### 本次固定验收清单

以下检查全部通过才算发布成功。`8444` 必须是 loopback 的严格 HTTPS；Kiosk 打开的
同源页面还必须持续接收 WSS 实时数据。HTTPS 健康成功而 HMI/WSS 不工作，仍是失败。

```bash
HMI=https://127.0.0.1:8444
HMI_CA=/usr/local/share/ca-certificates/block-dmp-blk-rel-001.crt

sudo readlink -f /opt/block/current
sudo cat /var/lib/block-release/current-version
sudo cat /var/lib/block-release/previous-release
for SERVICE in block.service block-kiosk.service ssh-bootstrapd.service; do
  sudo systemctl is-active "$SERVICE" | sudo tee "$BACKUP/$SERVICE.after" >/dev/null || true
  sudo diff -u "$BACKUP/$SERVICE.before" "$BACKUP/$SERVICE.after"
done

sudo curl --proto '=https' --tlsv1.2 --cacert "$HMI_CA" --fail --silent --show-error \
  "$HMI/healthz" >/dev/null
! sudo ss -ltnH | grep -Eq ':(8080|8081)([[:space:]]|$)'
sudo ss -ltnH | awk '$4 ~ /:(8443|9443)$/ { print $1, $4 }' | sort | \
  sudo tee "$BACKUP/maintenance-listeners.after" >/dev/null
sudo diff -u "$BACKUP/maintenance-listeners.before" "$BACKUP/maintenance-listeners.after"
```

账号、idle 和首次管理员状态也必须保留。只记录用户名与角色，绝不输出密码哈希：

```bash
sudo sqlite3 -noheader /var/lib/block/block.db \
  'PRAGMA integrity_check; SELECT username || "|" || role FROM local_accounts ORDER BY username; SELECT "idle=" || idle_timeout_seconds FROM local_system_settings WHERE singleton_id = 1;' | \
  sudo tee "$BACKUP/auth-after.txt" >/dev/null
sudo grep -Fxq ok "$BACKUP/auth-after.txt"
sudo diff -u "$BACKUP/auth-before.txt" \
  <(sudo sed '1{/^ok$/d;}' "$BACKUP/auth-after.txt")

sudo curl --proto '=https' --tlsv1.2 --cacert "$HMI_CA" --fail --silent --show-error \
  "$HMI/api/v2/auth/initial-admin" | sudo tee "$BACKUP/bootstrap-after.json" >/dev/null
sudo grep -Eq '"bootstrapRequired"[[:space:]]*:[[:space:]]*false' "$BACKUP/bootstrap-after.json"
sudo curl --proto '=https' --tlsv1.2 --cacert "$HMI_CA" --fail --silent --show-error \
  "$HMI/api/v2/config/session" | sudo tee "$BACKUP/idle-after.json" >/dev/null
sudo grep -Eq '"idleTimeoutSeconds"[[:space:]]*:[[:space:]]*[0-9]+' "$BACKUP/idle-after.json"
```

`ssh-bootstrapd.service` 只有设备原先已启用时才要求为 `active`；若它原先未启用，
before/after 比对仍必须一致。最后在真实 Kiosk 屏幕上确认 URL 为
`https://127.0.0.1:8444/`、页面不报证书错误并持续显示 WSS 更新。

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

### Chromium 对本机 CA 的受控信任（仅 `NET::ERR_CERT_AUTHORITY_INVALID` 时）

先执行上面的严格健康检查和 HMI HTTPS 验收；它们必须使用
`/usr/local/share/ca-certificates/block-dmp-blk-rel-001.crt`，不得使用 `-k`、`--insecure` 或
`--ignore-certificate-errors`。只有这些严格检查已经通过，并且 Chromium 真实显示
`NET::ERR_CERT_AUTHORITY_INVALID` 时，才允许设备管理员进行以下一次受控导入：

```bash
sudo systemctl stop block-kiosk.service
sudo test -d /home/block-ui/.pki/nssdb
sudo cp -a --no-dereference -- \
  /home/block-ui/.pki/nssdb "$BACKUP/block-ui-nssdb-before-local-ca"
sudo -u block-ui sh -c 'command -v certutil >/dev/null'
sudo -u block-ui certutil -d sql:/home/block-ui/.pki/nssdb \
  -A -n block-local-business-ca -t 'C,,' \
  -i /usr/local/share/ca-certificates/block-dmp-blk-rel-001.crt
sudo systemctl start block-kiosk.service
```

这不是安装器或 kiosk unit 的自动步骤。只使用设备已有的 `certutil`；不得安装额外
工具。`certutil` 不存在、NSS 数据库缺失、导入失败或浏览器仍出现证书错误时立即
STOP；不要改用忽略证书错误的 Chromium 参数。该步骤只导入公开 CA，绝不导入私钥。
若需撤销，应由设备管理员在已备份的 profile 范围内恢复
`block-ui-nssdb-before-local-ca`，然后重新执行严格 HTTPS 验收。

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

## 8. 2026-08-07 真机发布记录

首次候选安装、`8444` 严格 TLS 健康检查和候选事务链均已通过；验收阶段的旧
`verify-install.sh` 将合法的 `BLOCK_MQTTS_V2_ENDPOINT` 中的 `POINT` 误判为
持久化点位。安装器因此完整回滚到旧 release，未连续重试或手工绕过回滚。

随后以 `6a406cd` 修复精确点位键匹配并重新构建候选，第二次发布成功。最终设备版本为
`0.0.0-verify-install-fix`。本记录不提交真实设备备份、报告或截图：受保护的设备
备份留在设备上；工作区证据只可按相对缓存路径记录，例如
`.cache/device-release-candidate-deploy-bundle/deployment-attempt-report.md` 和同目录下的
截图，缓存不进入 Git。

## 9. 下次发布固定清单与停止点

1. 先完成第 3 节盘点，记录 current/previous、三个 service、端口与安全配置摘要。
2. 创建 root-only 备份，完成两个 unit、`block.env`、current release 与 SQLite 冷备；
   冷备完成后先恢复旧 Block、再恢复旧 kiosk。
3. 在 WSL/Linux 打包候选，传输后校验 manifest、ELF arm64 和全部 `deploy/*.sh`、
   `deploy/tests/*.sh` 执行位；模式不符就重新打包。
4. 只运行 `$STAGE/artifact/deploy/install.sh`，不调用旧 current installer，也不手动
   切换链接或重启服务。
5. 让安装器完成 TLS/config/unit 预检、snapshot 与事务安装；它失败时先核对已恢复的
   current、unit、配置和健康状态，不立即重试。
6. 按第 5 节完成 current/previous、三个 service、8444 HTTPS/WSS、8080/8081 缺失、
   8443/9443 不变、SQLite/accounts/idle/bootstrapRequired 和真实屏幕验收。

备份不完整、SQLite 冷备或完整性检查失败、manifest/ELF/执行位不符、候选预检失败、
安装器回滚、严格 TLS 或 WSS 验收失败、端口变化、账号/idle/bootstrap 状态变化，或
Chromium 不是已证实的 `NET::ERR_CERT_AUTHORITY_INVALID`，均为 STOP 条件。保留证据、
恢复既有服务状态，并在新的授权与明确故障原因前不重试。
