# Block v2 真机发布

本文件是 Block 的唯一真机发布流程。当前默认采用开发阶段流程；生产阶段恢复要求在
明确进入生产阶段后再启用。每次安装（包括首次迁移）必须从受控暂存的
候选制品执行 `artifact/deploy/install.sh`；绝不调用 `/opt/block/current/deploy/install.sh`
升级。安装成功后，该候选的同一份流程会随 release 复制到 `current/deploy/`。不要另建
手工发布流程、第二个 systemd 服务或临时发布工具。

本流程只发布 Block runtime 和本地 HMI，不配置 Wi-Fi、BDM、PLC 点位或 PLC
控制逻辑。整个发布过程不得向 PLC 写入任何值。

### 2026-08-08 开发阶段策略记录

自 2026-08-08 起，开发阶段发布不要求程序可恢复：普通程序发布不备份旧程序或
数据库，失败后修复并重新安装。只有删除账号、数据库迁移或直接修改业务数据等
破坏性数据库写操作，才在首次写入前备份数据库一次。生产备份与回滚要求待明确进入
生产阶段后另行启用；本记录不代表执行了一次新发布。

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

生成不可变制品。开发阶段可运行仓库已有的发布回归检查，但 rollback/transaction
相关测试不是发布前置门禁。`build.sh` 要求制品目录尚不存在。

```bash
cd "$BLOCK_REPO"
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
deploy/chromium/block-kiosk.json、deploy/install.sh、health-check.sh、systemd unit、配置示例及其引用的必要 helper
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

## 3. 开发阶段默认流程：真机只读盘点与数据备份边界

只有设备管理员在已批准的固定安装身份下才能进入本节。先创建一个发布记录，
随后所有命令均在目标机执行。先做只读盘点；身份、当前版本或服务状态有任何一
项与发布单不符时立即 STOP，不传输、不安装、不重启。

在目标机的同一个 shell 中重新设置发布版本。此值必须与第 2 节已构建并签核的
`VERSION` 完全一致，后续暂存、安装和验收命令都使用它。

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

创建 root 独占的发布证据目录，用于保存安装前后状态摘要；它不是程序或数据库备份：

```bash
STAMP=$(date -u +%Y%m%dT%H%M%SZ)
BACKUP="/var/backups/block/evidence-$VERSION-$STAMP"
sudo install -d -o root -g root -m 0700 "$BACKUP"
sudo stat -c '%U:%G:%a %n' "$BACKUP"

# 只记录摘要，不复制旧二进制、release、unit 或受保护配置。
sudo readlink -f /opt/block/current | sudo tee "$BACKUP/current.before" >/dev/null
sudo cat /var/lib/block-release/current-version | \
  sudo tee "$BACKUP/current-version.before" >/dev/null
sudo cat /var/lib/block-release/previous-release | \
  sudo tee "$BACKUP/previous-release.before" >/dev/null
for SERVICE in block.service block-kiosk.service ssh-bootstrapd.service; do
  sudo systemctl is-active "$SERVICE" | sudo tee "$BACKUP/$SERVICE.before" >/dev/null || true
done
sudo ss -ltnH | awk '$4 ~ /:(8443|9443)$/ { print $1, $4 }' | sort | \
  sudo tee "$BACKUP/maintenance-listeners.before" >/dev/null
sudo sqlite3 -noheader /var/lib/block/block.db \
  'PRAGMA integrity_check; SELECT username || "|" || role FROM local_accounts ORDER BY username; SELECT "idle=" || idle_timeout_seconds FROM local_system_settings WHERE singleton_id = 1;' | \
  sudo tee "$BACKUP/auth-before.txt" >/dev/null
sudo grep -Fxq ok "$BACKUP/auth-before.txt"
```

当前开发阶段不要求程序可恢复。普通 runtime 或 HMI 发布不要求为程序回退复制旧二进制、
release、unit、配置或 Chromium profile，也不备份 SQLite；发布失败时先定位和修复问题，
重新构建并取得新的设备写入授权后再安装。

只有删除账号、数据库迁移、直接修改业务数据等破坏性数据库写操作，才在首次数据库
写入前创建一次 SQLite 一致性备份，并执行 `PRAGMA integrity_check`。同一任务后续步骤
共用这份备份，不得按发布步骤重复备份；只有该备份之后又发生独立的数据迁移，才创建
新的数据库备份。设备自带 Python 3.6，不能假定存在 `sqlite3.Connection.backup`；需要
备份时使用下面的短暂停机冷备，不为此安装 Python 包或其他工具。若当前任务只是程序
发布，跳过下面的数据库备份命令：

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
sudo systemctl start block.service
sudo systemctl is-active --quiet block.service
sudo systemctl start block-kiosk.service
sudo systemctl is-active --quiet block-kiosk.service
```

破坏性数据库写操作所需的备份目录权限不是 `root:root 0700`、复制失败或完整性检查
失败时立即 STOP，不得执行该数据库写操作。普通程序发布不受此备份门禁约束。

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
sudo test -x "$STAGE/artifact/deploy/health-check.sh"
sudo test -f "$STAGE/artifact/deploy/systemd/block.service"
sudo test -f "$STAGE/artifact/deploy/systemd/block-kiosk.service"
sudo test -f "$STAGE/artifact/deploy/config/block.env.example"
for SCRIPT in \
  deploy/install.sh deploy/health-check.sh \
  deploy/install-users.sh deploy/version.sh deploy/verify-install.sh \
  deploy/verify-static.sh; do
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

若安装器返回失败，记录当前版本、service 状态和健康检查结果，定位并修复程序后重新
构建；取得新的设备写入授权后再安装。当前开发阶段不要求恢复旧程序，也不要求验证
自动或手工 rollback 成功后才能继续。

安装器仍须在写入配置、unit 或 `current` 链接之前严格验证本机证书、私钥和公开 CA。
现有安装器内部的 transaction、snapshot 和 rollback 行为允许保留，但它们不是开发
发布的前置门禁，也不得成为额外备份数据库的理由。

候选制品、候选 `deploy/`、本机 TLS 材料、配置和两个 unit 任一预检失败时，安装器
不得创建 release、写配置、切换 `current` 或停止现有服务。开发发布只把预检、安装
结果和发布后健康检查作为门禁，不检查 rollback/transaction 脚本是否存在或通过。

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
  "$HMI/api/auth/initial-admin" | sudo tee "$BACKUP/bootstrap-after.json" >/dev/null
sudo grep -Eq '"bootstrapRequired"[[:space:]]*:[[:space:]]*false' "$BACKUP/bootstrap-after.json"
sudo curl --proto '=https' --tlsv1.2 --cacert "$HMI_CA" --fail --silent --show-error \
  "$HMI/api/config/session" | sudo tee "$BACKUP/idle-after.json" >/dev/null
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
缓存修复步骤：

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

## 6. 开发阶段失败处理与生产阶段恢复要求

开发阶段下列任一情况都必须 STOP 并保留证据：目标身份不匹配、制品哈希不匹配、
安装器失败、健康检查失败、根路径不是 `200`、`no-store` 缺失、X11 PID/URL 不稳定、
屏幕仍非本次页面，或没有可用的真实屏幕采集方式。若任务包含破坏性数据库写操作，
其唯一数据库备份不完整或完整性检查失败也必须 STOP；普通程序发布没有数据库备份
门禁。程序安装失败后修复并重新安装，不要求程序回滚或恢复旧版本。

现有 rollback/transaction 脚本和安装器内部自动回滚可以保留，但其存在、测试通过或
成功恢复均不是开发发布门禁。将来明确进入生产阶段后，再启用下列程序恢复流程并按
当时批准的生产策略验证。生产阶段的手工回滚只适用于：安装器已成功返回、
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

生产阶段程序备份、回滚失败后的恢复和留存要求必须在进入生产阶段时另行批准，不能
沿用当前开发阶段的无程序备份流程。任何阶段都不得删除 release 目录、profile 或现场
数据来伪造恢复。

不要自动重试或连续重复安装。任何新的尝试都必须先说明故障原因、确认当前版本，并
取得新的设备写入授权；只有破坏性数据库写操作才需要同时确认数据库备份可用。

## 7. 发布报告与清理

完成或 STOP 后都要产生一份不含秘密的发布报告。生产阶段启用回滚后，回滚也必须
产生报告。报告至少包含：

- 发布单号、`siteId`、`blockId`、`deviceId`；
- 目标版本、提交、构建时间、制品哈希和暂存校验结果；
- 发布前后 current release、service/port/health 状态；
- 发布证据目录和暂存目录；若执行过破坏性数据库写操作，记录该任务唯一 SQLite
  备份及完整性检查结果；生产阶段启用回滚后再记录回滚结果；
- 原始 HTTPS 头文件位置、`/` 的 200/no-store 验收结果；
- 两次 X11 PID/URL 采集和最终实机屏幕截图位置；
- 全程未向 PLC 写入的确认。

报告不得包含私钥、密码、真实配置内容、Wi-Fi 内容、证书私钥、固定安装身份的
路径/内容/指纹，或可复用的 SSH 会话材料。

验收结束后，删除本次运行时创建的 SSH 私钥副本、known_hosts、临时 SSH 配置和
会话目录；不要删除设备管理员保存的原始固定身份。保留暂存证据、release 目录和本次
破坏性数据库写操作产生的唯一有效备份，直到设备管理员按留存策略确认可以清理。

## 8. 2026-08-07 真机发布记录

首次候选安装、`8444` 严格 TLS 健康检查和候选事务链均已通过；验收阶段的旧
`verify-install.sh` 将合法的 `BLOCK_MQTTS_V2_ENDPOINT` 中的 `POINT` 误判为
持久化点位。安装器因此完整回滚到旧 release，未连续重试或手工绕过回滚。

随后以 `6a406cd` 修复精确点位键匹配并重新构建候选，第二次发布成功。最终设备版本为
`0.0.0-verify-install-fix`。本记录不提交真实设备备份、报告或截图：受保护的设备
备份留在设备上；工作区证据只可按相对缓存路径记录，例如
`.cache/device-release-candidate-deploy-bundle/deployment-attempt-report.md` 和同目录下的
截图，缓存不进入 Git。

## 9. 开发阶段下次发布固定清单与停止点

1. 先完成第 3 节盘点，记录 current/previous、三个 service、端口与安全配置摘要。
2. 记录 Git 提交、制品版本与哈希。普通程序发布不备份旧程序或数据库；若任务包含
   删除账号等破坏性数据库写操作，在首次写入前仅做一次 SQLite 备份和完整性检查。
3. 在 WSL/Linux 打包候选，传输后校验 manifest、ELF arm64 和必要发布脚本执行位；
   模式不符就重新打包。
4. 只运行 `$STAGE/artifact/deploy/install.sh`，不调用旧 current installer，也不手动
   切换链接或重启服务。
5. 让安装器完成 TLS/config/unit 预检和安装；它失败时记录现状、定位并修复问题，
   重新构建并取得新的设备写入授权后再安装，不要求程序回滚。
6. 按第 5 节完成 current/previous、三个 service、8444 HTTPS/WSS、8080/8081 缺失、
   8443/9443 不变、SQLite/accounts/idle/bootstrapRequired 和真实屏幕验收。

manifest/ELF/执行位不符、候选预检失败、严格 TLS 或 WSS 验收失败、端口变化、
账号/idle/bootstrap 状态变化，或 Chromium 不是已证实的
`NET::ERR_CERT_AUTHORITY_INVALID`，均为 STOP 条件。破坏性数据库写任务的唯一备份或
完整性检查失败同样 STOP。保留证据，并在新的授权与明确故障原因前不重试；普通程序
发布不要求恢复旧程序状态。

## 10. 2026-08-07 Kiosk 原生界面与输入响应验收

`deploy/chromium/block-kiosk.json` 是随 release 版本化的 Chromium 受管策略。安装器会把它写入
`/etc/chromium-browser/policies/managed/block-kiosk.json`，并将安装前的该文件纳入事务快照；自动或手工
回滚时恢复对应快照。策略关闭密码保存、地址/银行卡自动填充和翻译，并拒绝默认弹窗、通知、定位与媒体权限。
Kiosk unit 仅使用 `--no-default-browser-check`、`--noerrdialogs`、`--deny-permission-prompts` 等受控参数，保持
`https://127.0.0.1:8444/?performance=1` 的严格 TLS；禁止加入 `--ignore-certificate-errors`、
`--allow-insecure-localhost`、`--disable-web-security` 或任何绕过本机 CA 校验的参数。

HMI 不再请求浏览器全屏、右键菜单或 `alert`/`confirm`/`prompt` 原生对话框。首次管理员创建采用单飞请求：
接口成功返回 `201` 后直接建立前端内存会话，不再回跳登录或重复调用登录接口。软键盘或认证输入期间，PLC/WSS
与本机后端轮询继续接收最新数据，但合并延后全页渲染；关闭软键盘或离开输入后仅刷新一次最新状态。该取舍以
“操作响应优先于装饰效果”为准，不改变 PLC 采集和本地自治。

设备管理员在真实 Kiosk 屏幕完成常规 HTTPS/WSS 验收后，还应确认：无首次运行、保存密码、自动填充、翻译、
权限或浏览器错误提示；首个管理员提交后一次进入会话；连续软键盘输入无明显卡顿，关闭键盘后页面数据保持最新。
候选制品仍按第 2 节生成并校验 `$CACHE_ROOT/artifact.sha256`，发布报告同时记录版本、该 manifest 的 SHA-256 与
`artifact/bin/block-agent` 的 SHA-256；不得记录密码、私钥或真实 Wi-Fi 配置。

## 11. 2026-08-08 HMI 模式切换与现场显示验收

本次更新 Block HMI：移除了页面中对现场用户可见的 `V2`/`v2` 文案；本机认证和维护 HTTP 路径已硬切换为
`/api/...`。`/api/v1/...` 和 `/api/v2/...` 没有别名、重定向或回退，请求必须被拒绝为 404。MQTTS v2 的协议、配置名称和契约标识保持不变。

模式切换继续使用既有本地 WSS 点位链路。访客点击自动/手动模式时只打开登录，不发送
运行时命令；ADMIN 或 OPERATOR 登录且 PLC 已连接后，HMI 将
`home.machine.enabled` 映射为 `machine.enabled` 的 `point.command` `toggle`。Agent
和 PLC 的既有 mask-write/toggle 语义保持不变，HMI 不创建本地模拟状态：自动/手动及其
绿色/黄色外观均由后续 PLC 当前点位回显确定。

开发验收至少覆盖：游客门禁、自动→手动与手动→自动的 `point.command`/`point.result`
成功路径、PLC 写入失败、结果超时和连接中断。失败必须显示 HMI 页面 toast，不能显示
浏览器原生对话框。该前端改动不含真实设备写入；按开发阶段策略完成源代码测试、制品
构建、版本和哈希记录即可，不以程序回滚为发布门禁。

## 12. 2026-08-08 真机 PLC 闭环验收记录

版本 `0.0.0-hmi-mode-race-20260808` 已完成 Block HMI、正式 WSS 与 PC 模拟 PLC
闭环验收，结果为 PASS。测试覆盖 50 ms FC03 扫描、外部改值通知、两次 FC22 模式
toggle、邻位保持和 100 ms 脉冲；本轮开发发布未做程序或数据库备份。网络、数值、
证据边界、临时配置清理命令和测试结束时模拟器状态见
[2026-08-08 Block HMI 与 PC 模拟 PLC 真机闭环测试报告](device-plc-closed-loop-2026-08-08.md)。

## 13. 2026-08-08 旧可靠上行源码清理记录

本次仅清理已经不可达的旧可靠上行源码、配置和 SQLite 表注册，保留 MQTTS v2
当前状态与报警历史只读同步。新增清理迁移只会在本地数据库打开时删除已退役的
`mqtt_outbound_inflight`、`uplink_gap_ledger`、`uplink_outbox` 和
`uplink_stream_state` 表；不修改账号、PLC 数据、报警历史或 MQTTS v2 数据。

本记录对应源代码级验证和临时 SQLite 测试；未生成或重建候选制品，未执行部署、
设备连接或真实数据库操作。实际设备数据库迁移仅可由获授权的设备管理员在其发布
流程中执行，并按当时的破坏性数据写入规则处理。

## 14. 2026-08-08 API 清理版真机发布记录

版本 `0.0.0-api-cleanup-20260808` 已完成直接发布、旧 MQTT v1 表清理、无版本
Local API、严格 TLS/WSS、HMI installed web 一致性和 PLC 短回归，结果为 PASS。
本轮未保留候选 archive，未做程序或数据库备份，未创建 previous 或 snapshot；本地和
设备临时 stage 均已删除。完整结果与证据路径见
[2026-08-08 Block API 清理版真机发布报告](device-api-cleanup-release-2026-08-08.md)。
