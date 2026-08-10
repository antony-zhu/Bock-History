# Block 代码、编译、设备部署与 PLC 模拟测试指南

本文说明 Block 本地 HMI、Block Agent、应用制品发布和 Easy521 电脑模拟 PLC 的当前推荐流程。命令以 `2026-08-09` 已验证的工作树和脚本为准。

当前运行时固定在每次完整 PLC 读取结束后等待 500 ms 再开始下一次读取。普通成功写后立即完整轮询；脉冲按置 1 → 100 ms → 复位 0 → 立即完整轮询，读取完成才算命令完成。实际 PLC 写入失败或超时也立即完整轮询一次，读取完成后才回复失败；随后从该次读取完成时重新等待 500 ms。无效命令和本地校验失败不会额外读取。本指南中标明“历史”的 50 ms 记录仅保留当时的验收事实，不能作为当前 500 ms 的真机验收证据。

这是一套 **Block 应用部署** 流程，不是固件烧写流程。它不写 bootloader、kernel、rootfs、分区或整机镜像，也不修改 PLC 控制程序。

## 1. 路径、版本和不可越过的边界

| 内容 | 路径或值 |
| --- | --- |
| Block 正式 Git 仓库 | `D:\codex\Block-DMP\repos\Block` |
| 当前开发工作树 | `D:\codex\Block-DMP\repos\Block` |
| 本轮冻结的应用提交 | `fc685ad30f0141c8e8be3604cea48cb0545809f1`（基于 `b1b2862`） |
| Common baseline | `d1073038277db0b954c021cb2cc377012ec8a78c` |
| HMI | `apps/block-hmi/**` |
| Agent | `services/block-agent/**` |
| 正式构建和安装脚本 | `deploy/block/**` |
| Easy521 电脑模拟器 | `D:\codex\Block-DMP\imports\easy521-plc-simulator` |
| 正式手动页模拟点表/种子 | `manual_page_points.json`、`manual_page_seed.json`，位于上述模拟器目录 |

开始前执行：

```powershell
# Windows PowerShell
$BlockRepo = 'D:\codex\Block-DMP\repos\Block'
git -C $BlockRepo status --short
git -C $BlockRepo rev-parse HEAD
Get-Content -LiteralPath "$BlockRepo\COMMON_BASELINE" -Encoding UTF8
```

发布必须冻结一个明确提交、一个未复用的新版本字符串和一个单一制品。工作树出现未批准的 tracked 修改时停止。不要读取或输出 `wifi.toml`、真实 `.env`、密码、私钥、证书私钥、token 或真实安装身份路径。

设备示例只允许受信管理地址 `192.168.1.104`。IP 只用于连接，不能代替 `siteId`、`blockId` 和 `deviceId`。

## 2. PC 上预览 Apple Style 手动页

正式本地预览工具会构建并启动真实 `block-agent`，通过严格的回环 HTTPS/WSS 提供当前工作树 HMI；它不是纯静态 HTTP 预览。

```powershell
# Windows PowerShell，在工作树根目录执行
Set-Location 'D:\codex\Block-DMP\repos\Block'
.\tools\start-block-hmi-auth-demo.ps1
```

浏览器打开：

- 访客手动页：`https://127.0.0.1:8444/?demo=1`
- 操作员手动页：`https://127.0.0.1:8444/?demo=1&manualRole=operator`
- 管理员手动页：`https://127.0.0.1:8444/?demo=1&manualRole=admin`

停止时只使用同一工作树脚本：

```powershell
.\tools\start-block-hmi-auth-demo.ps1 -Stop
```

`?demo=1` 只控制浏览器演示状态；它不会连接或配置真实 Wi-Fi，也不会切换发布点表。预览脚本直接使用工作树中的默认 `apps/block-hmi/assets/points.json`。

### 两种 PLC profile

| profile | 点位 | 用途和安全含义 |
| --- | --- | --- |
| `default` | 18 BOOL + 1 FLOAT32/REAL + 5 INT16 + 3 INT32/DINT，共 27 点、54 bindings，`scanIntervalMs=500` | 默认构建；未显式设置环境变量时使用的获批真实点表。 |
| `simulatorFloat32` | 8 BOOL + 22 FLOAT32，共 30 点 | 仅可显式选择的 legacy 电脑模拟 PLC 联调 profile；FLOAT32 是本机模拟约定，不得当成真实 Easy521 字序、权限或动作语义。 |

构建器只接受这两个值；未知 profile 会直接拒绝。无论选择哪种 profile，制品中都只保留一份 `web/assets/points.json`，不会把 `points.simulatorFloat32.json` 源文件一并发布。

## 3. 本地缓存和测试

所有临时目录和 Go 缓存必须留在 `D:\codex\Block-DMP\.cache\**`。下面使用一个独立任务目录；不要复用用户目录或仓库外缓存。

```powershell
# Windows PowerShell
$Workspace = 'D:\codex\Block-DMP'
$BlockRepo = "$Workspace\repos\Block"
$CacheRoot = "$Workspace\.cache\block-guide-validation"
$Go = "$Workspace\.tools\go1.26.5\go\bin\go.exe"

New-Item -ItemType Directory -Force -Path `
  "$CacheRoot\tmp", "$CacheRoot\gotmp", "$CacheRoot\gocache", `
  "$CacheRoot\gomodcache", "$CacheRoot\bin" | Out-Null

$env:TEMP = "$CacheRoot\tmp"
$env:TMP = $env:TEMP
$env:TMPDIR = $env:TEMP
$env:GOTMPDIR = "$CacheRoot\gotmp"
$env:GOCACHE = "$CacheRoot\gocache"
$env:GOMODCACHE = "$CacheRoot\gomodcache"
```

新建的 `GOMODCACHE` 首次使用前，先完成第 4 节“首次准备锁定依赖”，再回到本节执行 Go 测试；依赖准备完成后可在 PowerShell 设置 `$env:GOPROXY = 'off'`，确保测试不再联网。

### TypeScript、Node 和 HMI Go 测试

```powershell
Set-Location "$BlockRepo\apps\block-hmi"
tsc assets/hmi.mts --target ES2022 --lib DOM,ES2022 `
  --module NodeNext --moduleResolution NodeNext --strict `
  --skipLibCheck --noEmitOnError --outDir assets
node assets/hmi.test.mjs
& $Go test ./...
```

`deploy/block/build.sh` 复制已经存在的 HMI 产物，不会替 Release 编译 `hmi.mts`。编译后若 `assets/hmi.mjs` 出现未批准差异，应先提交和审查代码，不能直接把变化中的文件打入发布包。

### Agent 测试

```powershell
Set-Location "$BlockRepo\services\block-agent"
& $Go test ./...
& $Go vet ./...
```

需要完整并发检查时，再在同一缓存环境执行 `& $Go test -race ./...`。

### Easy521 模拟器测试

```powershell
Set-Location 'D:\codex\Block-DMP\imports\easy521-plc-simulator'
python -m unittest discover -v
```

该测试使用动态端口，不连接真实 PLC。

## 4. Windows portable Go + Git Bash 构建 Linux ARM64

本轮实际可用组合是工作区 portable Go `go1.26.5 windows/amd64`、Git Bash 和 Go 的 `GOOS=linux/GOARCH=arm64` 交叉编译。不要让 WSL 调用 Windows Go 包装器；打包前必须独立确认输出是 AArch64 ELF，而不是 Windows PE。

先从 PowerShell 启动 Git Bash：

```powershell
& 'C:\Program Files\Git\bin\bash.exe'
```

以下命令在 **Git Bash** 执行。`VERSION` 必须替换为获批且未复用的新字符串；版本只允许字母、数字、点、下划线和短横线。

```bash
WORKSPACE=/d/codex/Block-DMP
BLOCK_REPO="$WORKSPACE/repos/Block"
VERSION='<approved-unique-version>'
CACHE_ROOT="$WORKSPACE/.cache/block-release-$VERSION"
ARTIFACT_DIR="$CACHE_ROOT/artifact"

mkdir -p "$CACHE_ROOT/tmp" "$CACHE_ROOT/gotmp" \
  "$CACHE_ROOT/gocache" "$CACHE_ROOT/gomodcache"
export PATH="$WORKSPACE/.tools/go1.26.5/go/bin:$PATH"
export TEMP="$CACHE_ROOT/tmp"
export TMP="$CACHE_ROOT/tmp"
export TMPDIR="$CACHE_ROOT/tmp"
export GOTMPDIR="$CACHE_ROOT/gotmp"
export GOCACHE="$CACHE_ROOT/gocache"
export GOMODCACHE="$CACHE_ROOT/gomodcache"
```

### 首次准备锁定依赖

只有首次填充本任务 `GOMODCACHE` 时联网。使用 `go.mod/go.sum` 已锁定依赖，保持 checksum database、`go.sum` 和 TLS 校验；不得设置 `GONOSUMDB`、`GOINSECURE` 或修改依赖文件。

```bash
export GOPROXY=https://goproxy.cn,direct
export GOSUMDB=sum.golang.org
unset GONOSUMDB GOINSECURE

cd "$BLOCK_REPO/services/block-agent"
go mod download
go mod verify
git -C "$BLOCK_REPO" diff --exit-code -- services/block-agent/go.mod services/block-agent/go.sum
```

下载失败时停止，不安装系统 Go、不下载未锁定工具、不改 `go.mod/go.sum`。

### 离线构建单一制品

锁定依赖准备完成后关闭模块网络访问：

```bash
export GOPROXY=off
export CGO_ENABLED=0
export GOOS=linux
export GOARCH=arm64
cd "$BLOCK_REPO"
```

每次发布只选择下面一条。

默认安全 profile：

```bash
unset BLOCK_PLC_PROFILE
deploy/block/build.sh --output "$ARTIFACT_DIR" --version "$VERSION"
```

仅可显式选择的 legacy 电脑模拟 PLC profile：

```bash
export BLOCK_PLC_PROFILE=simulatorFloat32
deploy/block/build.sh --output "$ARTIFACT_DIR" --version "$VERSION"
```

`ARTIFACT_DIR` 必须事先不存在。不要在同一版本下反复覆盖或生成多个候选。

## 5. 制品校验和 WSL 打包

先在 PowerShell 对点表做结构计数：

```powershell
$ArtifactDir = 'D:\codex\Block-DMP\.cache\block-release-<version>\artifact'
$Profile = Get-Content -LiteralPath "$ArtifactDir\web\assets\points.json" `
  -Raw -Encoding UTF8 | ConvertFrom-Json
$BoolCount = @($Profile.points | Where-Object type -eq 'bool').Count
$FloatCount = @($Profile.points | Where-Object type -eq 'float32').Count
$Int16Count = @($Profile.points | Where-Object type -eq 'int16').Count
$Int32Count = @($Profile.points | Where-Object type -eq 'int32').Count
$BindingCount = @($Profile.bindings).Count
[pscustomobject]@{
  Total = @($Profile.points).Count
  Bool = $BoolCount
  Float32 = $FloatCount
  Int16 = $Int16Count
  Int32 = $Int32Count
  Bindings = $BindingCount
  ScanIntervalMs = $Profile.scanIntervalMs
  SimulatorSourceLeaked = Test-Path -LiteralPath `
    "$ArtifactDir\web\assets\points.simulatorFloat32.json"
}
```

预期：`default` 为 `Total=27`、`Bool=18`、`Float32=1`、`Int16=5`、`Int32=3`、`Bindings=54`、`ScanIntervalMs=500`；仅显式选择的 legacy `simulatorFloat32` 保持 `Total=30`、`Bool=8`、`Float32=22`，且 `SimulatorSourceLeaked=False`。

Windows `tar.exe` 可能丢失脚本执行位，因此在 **WSL/Linux shell** 生成 manifest 和唯一压缩包：

```bash
WORKSPACE=/mnt/d/codex/Block-DMP
VERSION='<same-approved-version>'
CACHE_ROOT="$WORKSPACE/.cache/block-release-$VERSION"
ARTIFACT_DIR="$CACHE_ROOT/artifact"

file "$ARTIFACT_DIR/bin/block-agent" | grep -Eq 'ELF .*ARM aarch64'
test "$(cat "$ARTIFACT_DIR/VERSION")" = "$VERSION"

(
  cd "$ARTIFACT_DIR"
  find . -type f -print0 | sort -z | xargs -0 sha256sum
) > "$CACHE_ROOT/artifact.sha256"

cd "$CACHE_ROOT"
tar --format=posix -czf artifact.tar.gz artifact artifact.sha256
tar -tzf artifact.tar.gz >/dev/null
sha256sum artifact.tar.gz artifact.sha256 artifact/bin/block-agent
```

发布记录只保存版本、源码提交、Common baseline、manifest/binary/archive SHA-256 和验收结论；不要把制品、日志或目标机 binary 提交 Git。

## 6. 设备写前基线和严格 SSH

设备写入只能由 Release/设备管理员执行。下面的 identity 和 pinned known_hosts 必须由管理员在运行时注入；占位符不能替换成仓库中的猜测路径。

```powershell
# Windows PowerShell；先替换两个占位符，不要把替换值写入文档或报告
$Device = '192.168.1.104'
$Identity = '<approved-fixed-install-identity>'
$PinnedKnownHosts = '<approved-pinned-known-hosts>'
$Remote = "root@$Device"
$SshOptions = @(
  '-F', 'NUL',
  '-o', "HostName=$Device",
  '-o', 'BatchMode=yes',
  '-o', 'IdentitiesOnly=yes',
  '-o', 'StrictHostKeyChecking=yes',
  '-o', "UserKnownHostsFile=$PinnedKnownHosts",
  '-i', $Identity
)

& ssh @SshOptions $Remote '/bin/true'
if ($LASTEXITCODE -ne 0) { throw 'Strict SSH preflight failed.' }
```

`-F NUL` 防止用户 SSH 配置重写目标。host key、身份或权限不匹配时停止，不能接受新 key、关闭校验、修私钥 ACL 或换用未知身份。

写入前先只读记录：

```bash
sudo /opt/block/current/deploy/version.sh
sudo readlink -f /opt/block/current
sudo cat /var/lib/block-release/current-version
sudo cat /var/lib/block-release/previous-release
for service in block.service block-kiosk.service ssh-bootstrapd.service; do
  printf '%s=' "$service"
  sudo systemctl is-active "$service" || true
done
sudo ss -ltnp | grep -E ':(22|8443|8444|9443)([[:space:]]|$)'
! sudo ss -ltnp | grep -Eq ':(80|1883|8080|8081)([[:space:]]|$)'
```

另记录安全的业务身份摘要、PLC endpoint/Unit/连接状态和 Wi-Fi 配置文件是否存在；只记录 Wi-Fi 文件存在性，绝不读取内容。不碰 BDM、MQTT、Wi-Fi 或数据库。

## 7. 临时 staging、正式安装和安装后验证

以下仍由 Release/设备管理员执行。先在 PowerShell 生成唯一 staging 名称并传输同一压缩包：

```powershell
$Version = '<same-approved-version>'
if ($Version -notmatch '^[A-Za-z0-9._-]+$') { throw 'Invalid version.' }
$Stamp = (Get-Date).ToUniversalTime().ToString('yyyyMMddTHHmmssZ')
$Stage = "/var/backups/block/stage-$Version-$Stamp"
$Archive = "D:\codex\Block-DMP\.cache\block-release-$Version\artifact.tar.gz"

& ssh @SshOptions $Remote "sudo install -d -o root -g root -m 0700 '$Stage'"
if ($LASTEXITCODE -ne 0) { throw 'Staging creation failed.' }
& scp @SshOptions $Archive "${Remote}:$Stage/artifact.tar.gz"
if ($LASTEXITCODE -ne 0) { throw 'Artifact transfer failed.' }
```

设备端只解包、校验并运行 **该 artifact 内** 的安装器：

```bash
VERSION='<same-approved-version>'
STAGE='<exact-stage-created-above>'
case "$VERSION" in ''|*[!A-Za-z0-9._-]*) exit 1 ;; esac
case "$STAGE" in /var/backups/block/stage-"$VERSION"-*) ;; *) exit 1 ;; esac

sudo tar -xzf "$STAGE/artifact.tar.gz" -C "$STAGE" --no-same-owner
sudo install -m 0640 -o root -g block /etc/block/block.env "$STAGE/block.env"
sudo sh -c "cd '$STAGE/artifact' && sha256sum -c ../artifact.sha256"
sudo file "$STAGE/artifact/bin/block-agent" | grep -Eq 'ELF .*ARM aarch64'
test "$(sudo cat "$STAGE/artifact/VERSION")" = "$VERSION"
sudo test -x "$STAGE/artifact/deploy/install.sh"
sudo test -x "$STAGE/artifact/deploy/health-check.sh"
sudo test -f "$STAGE/artifact/web/assets/points.json"

sudo "$STAGE/artifact/deploy/install.sh" --execute \
  --artifact-dir "$STAGE/artifact" \
  --config "$STAGE/block.env" \
  --version "$VERSION"
```

不要调用旧 `/opt/block/current/deploy/install.sh` 升级，不要手工创建 release、切换 `current`、修包执行位或并行重启服务。

安装成功后：

```bash
sudo /opt/block/current/deploy/verify-install.sh --expected-version "$VERSION"
sudo /opt/block/current/deploy/version.sh
sudo systemctl is-active block.service block-kiosk.service ssh-bootstrapd.service
sudo sha256sum /opt/block/current/bin/block-agent

HMI=https://127.0.0.1:8444
HMI_CA=/usr/local/share/ca-certificates/block-dmp-blk-rel-001.crt
sudo curl --proto '=https' --tlsv1.2 --cacert "$HMI_CA" \
  --fail --silent --show-error "$HMI/healthz"
sudo ss -ltnp | grep -E ':(22|8443|8444|9443)([[:space:]]|$)'
! sudo ss -ltnp | grep -Eq ':(80|1883|8080|8081)([[:space:]]|$)'
sudo journalctl -u block.service -b --no-pager
sudo journalctl -u block-kiosk.service -b --no-pager
```

端口职责不能混淆：

- `127.0.0.1:8444`：设备本机 HMI、API、health 和 WSS `/ws`；绝不对外开放。
- `0.0.0.0:8443`：独立维护 HTTPS，不提供 `/ws`。
- `9443`：独立 SSH bootstrap HTTPS，不是 HMI/WSS。

最后在真机屏幕确认 Chromium 打开 `https://127.0.0.1:8444/`、无证书错误、页面不是白屏并持续收到 WSS 更新。通过 HMI PLC 页和模拟器 `/api/status` 交叉确认 endpoint、Unit、连接状态、每次完整读取结束后等待 500 ms 的扫描节奏和 FC03 请求；没有单独写入授权时到此停止。

### 完成后清理

先验证 staging 名称属于本次版本，再删除设备临时 staging：

```bash
case "$STAGE" in
  /var/backups/block/stage-"$VERSION"-*) ;;
  *) printf 'unsafe staging path\n' >&2; exit 1 ;;
esac
sudo rm -rf -- "$STAGE"
```

随后删除本地本次 `artifact/`、`artifact.sha256`、`artifact.tar.gz` 和传输临时文件；保留 Git 源码、已安装 release 及无秘密报告。普通应用发布不备份数据库或旧程序，也不修改数据库。

## 8. 当前电脑模拟 PLC

本轮收尾时的运行实例：

| 项目 | 值 |
| --- | --- |
| PID | `45928` |
| Modbus TCP | `192.168.1.87:1502`，Unit `1` |
| 管理页/API | `http://127.0.0.1:8875/` |
| 运行点表 | `.cache/device-manual-plc-release-20260809/simulator/manual_page_points.json` |
| 状态 | `.cache/device-manual-plc-release-20260809/simulator/manual_page_state.json` |
| 日志 | 同目录 `simulator.stdout.log`、`simulator.stderr.log` |

PID 会变化，操作前必须核对命令行和端口所有者：

```powershell
$PidToCheck = 45928
Get-CimInstance Win32_Process -Filter "ProcessId = $PidToCheck" |
  Select-Object ProcessId, ExecutablePath, CommandLine
Get-NetTCPConnection -State Listen |
  Where-Object OwningProcess -eq $PidToCheck |
  Select-Object LocalAddress, LocalPort, OwningProcess
```

浏览器直接打开 `http://127.0.0.1:8875/` 可查看点位、客户端数和 FC03/FC10/FC22 通信记录。PowerShell 查看状态：

```powershell
Invoke-RestMethod -Uri 'http://127.0.0.1:8875/api/status'
```

以只读位置反馈 `D850` 为例，模拟一次外部 PLC 值变化：

```powershell
$Body = @{
  point = 'D850'
  value = 47.25
  dataType = 'FLOAT32'
  wordOrder = 'low-high'
} | ConvertTo-Json -Compress

Invoke-RestMethod -Method Post `
  -Uri 'http://127.0.0.1:8875/api/point/write' `
  -ContentType 'application/json' -Body $Body
```

不要把管理 HTTP 绑定改为非回环地址。不要用管理 API 猜测真实 PLC 字序、权限或工程单位；BOOL 写入只允许在单独批准的模拟测试白名单中执行。

如需停止或重启实例，先确认 PID、命令行和两个监听都属于该实例，再停止精确 PID。Windows 后台重新启动必须使用 `Start-Process -WindowStyle Hidden`，继续使用 imports 中的 `server.py`、正式 points/seed 和任务 cache 状态文件，并把 stdout/stderr 写到任务 cache。测试成功后默认保留新实例供现场继续观察。

## 9. 本轮实际结果和下一次最短验证

本轮设备版本为 `simfloat32-manual-20260809.1`。结果必须分层描述：

| 层级 | 结果 |
| --- | --- |
| ARM64 构建、manifest/archive 校验 | PASS |
| `.104` 安装、`verify-install`、版本、services、严格 TLS、明文拒绝 | PASS |
| 设备 Agent 到 PC 模拟器的连接和 30 点 FC03 读取扫描 | PASS |
| 本地 Easy521 client + PLC worker 写项目 | 历史 PASS（当时为 50 ms scan）：D850 外部变化、D800/D812 FC10 回读、D504 FC22 邻位保持、D550.3/.4 约 100 ms 脉冲、断线检测；当前 500 ms 节奏需按本指南重新验证 |
| 本地模拟器重启恢复 | NOT VERIFIED：外部重启晚于原 harness 的 8 秒判定窗，不能据此判定产品不重连 |
| 设备 WSS 点位写闭环 | **NOT VERIFIED**：点位命令总数 `0`；17 numeric、D504 邻位、D550.3/.4 和断线恢复均未在设备 WSS 路径执行 |

Python 手写 WSS 客户端在 `ready` 前失败，只能判为临时测试工具失败；真机 kiosk 与 `127.0.0.1:8444` 的连接保持正常，因此不能把该失败写成产品 WSS 失败。

下一次最短验证步骤：

1. 先由设备管理员取得可用、合规且严格 pinned 的设备 SSH 会话；认证失败时停止，不改 ACL、不关闭 host-key 校验。
2. 二选一：
   - 在 `services/block-agent` 现有 Go module 上下文使用仓库已锁定的 WebSocket 库构建 Linux ARM64 临时 harness，设备本机严格校验 CA 和 `127.0.0.1` hostname 后连接 `wss://127.0.0.1:8444/ws`；或
   - 直接在真机 HMI 上人工执行获批白名单操作，同时从 `http://127.0.0.1:8875/` 观察 FC03/FC10/FC22 和点值。
3. 只验证既定项目：D850/D852 外部变化、17 个允许 numeric 写回读、D504 FC22 邻位保持、D550.3/.4 100 ms 脉冲、模拟器断线后自动恢复。
4. 测试完成即删除设备临时 harness/staging，保留模拟器和无秘密报告。

不要再使用 Python 手写 WebSocket 握手或帧，不要开放 8444、把 8443 当 WSS、修改服务配置、防火墙或产品架构。

## 10. 应用部署与固件烧写

本流程的输入是 `block-agent` ARM64 ELF、HMI web、deploy helpers 和 `VERSION`，写入 `/opt/block/releases/$VERSION` 并由 `/opt/block/current` 激活。它不包含板级镜像，也不运行固件刷写工具。

如果未来需要固件烧写，必须另开任务，由设备/固件管理员提供准确板型、获批镜像、哈希、刷写工具、恢复和验收流程；不能从 `artifact.tar.gz` 或 `install.sh` 推导。

## 11. 正式依据

- [`apps/block-hmi/README.md`](../../apps/block-hmi/README.md)
- [`services/block-agent/README.md`](../../services/block-agent/README.md)
- [`deploy/block/README.md`](../../deploy/block/README.md)
- [`deploy/block/build.sh`](../../deploy/block/build.sh)
- [`deploy/block/install.sh`](../../deploy/block/install.sh)
- [`deploy/block/verify-install.sh`](../../deploy/block/verify-install.sh)
- 工作区 `.cache/manual-release-guide-20260809/inventory.md`
- 工作区 `.cache/device-manual-plc-release-20260809/report.md`
- 工作区 `.cache/manual-plc-live-test-20260809/report.md`
- 工作区 `.cache/manual-plc-sim-20260809/README.md`
- `imports/easy521-plc-simulator/README.md`
