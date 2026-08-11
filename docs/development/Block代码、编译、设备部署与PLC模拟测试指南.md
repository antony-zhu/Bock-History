# Block 代码、编译、设备部署与 PLC 模拟测试指南

本文说明 Block 本地 HMI、Block Agent、应用制品发布和 Easy521 电脑模拟 PLC 的当前推荐流程。构建命令以 `2026-08-11` 的可复现入口为准；下文保留的设备结果均是历史证据，不是新 clone 的工具链或缓存前提。

当前运行时固定在每次完整 PLC 读取结束后等待 500 ms 再开始下一次读取。普通成功写后立即完整轮询；脉冲按置 1 → 100 ms → 复位 0 → 立即完整轮询，读取完成才算命令完成。实际 PLC 写入失败或超时也立即完整轮询一次，读取完成后才回复失败；随后从该次读取完成时重新等待 500 ms。无效命令和本地校验失败不会额外读取。本指南中标明“历史”的 50 ms 记录仅保留当时的验收事实，不能作为当前 500 ms 的真机验收证据。

这是一套 **Block 应用部署** 流程，不是固件烧写流程。它不写 bootloader、kernel、rootfs、分区或整机镜像，也不修改 PLC 控制程序。

## 1. 路径、版本和不可越过的边界

| 内容 | 路径或值 |
| --- | --- |
| Block 正式 Git 仓库 | 当前 clone 的仓库根目录 |
| 当前开发工作树 | 当前 clone 的仓库根目录 |
| 归档盘点的源码范围 | `b1b2862` 至 `40bbe8e`；设备候选与真机证据须另见 [归档盘点](Block归档盘点-20260811.md) |
| Common baseline | `d1073038277db0b954c021cb2cc377012ec8a78c` |
| HMI | `apps/block-hmi/**` |
| Agent | `services/block-agent/**` |
| 正式构建和安装脚本 | `deploy/block/**` |
| Easy521 电脑模拟器 | `D:\codex\Block-DMP\imports\easy521-plc-simulator` |
| 正式手动页模拟点表/种子 | `manual_page_points.json`、`manual_page_seed.json`，位于上述模拟器目录 |

开始前执行：

```powershell
# Windows PowerShell；在 Block 仓库根目录执行
$BlockRepo = (Get-Location).Path
git -C $BlockRepo status --short
git -C $BlockRepo rev-parse HEAD
Get-Content -LiteralPath "$BlockRepo\COMMON_BASELINE" -Encoding UTF8
```

发布必须冻结一个明确提交、一个未复用的新版本字符串和一个单一制品。工作树出现任何已修改或未跟踪文件时停止。不要读取或输出 `wifi.toml`、真实 `.env`、密码、私钥、证书私钥、token 或真实安装身份路径。

本指南中的 `192.168.1.104` 是登记且受信的有线管理地址。IP 只用于连接，不能代替 `siteId`、`blockId` 和 `deviceId`；Wi-Fi 地址应按第 6 节的 DHCP 发现流程确认。

## 2. PC 上预览 Apple Style 手动页

正式本地预览工具会构建并启动真实 `block-agent`，通过严格的回环 HTTPS/WSS 提供当前工作树 HMI；它不是纯静态 HTTP 预览。

```powershell
# Windows PowerShell，在仓库根目录执行
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
首次没有显式 TLS 文件时，预览脚本需要系统 OpenSSL（Git for Windows 自带版本可用）；否则必须传入开发用证书、私钥和 CA 三个路径。
预览构建会清除继承的 `GIT_CONFIG_*`、Node TLS 覆盖和 npm 配置，并在启动成功或失败后恢复调用
PowerShell 原有环境，避免将工具链、缓存或用户 Git 配置泄漏给后续命令。
首次 `-FreshAuth` 可安全初始化不存在或空的 `DataDirectory`；已有非空目录必须带本 demo 的
owner marker 才会被删除。

### 两种 PLC profile

| profile | 点位 | 用途和安全含义 |
| --- | --- | --- |
| `default` | 33 BOOL + 1 FLOAT32/REAL + 5 INT16 + 3 INT32/DINT，共 42 点、54 bindings，`scanIntervalMs=500`；其中 15 个 BOOL 是报警 | 默认构建；未显式设置环境变量时使用的获批真实点表。15 条报警是源码定义数量，不等于真机已验收。 |
| `simulatorFloat32` | 8 BOOL + 22 FLOAT32，共 30 点 | 仅可显式选择的 legacy 电脑模拟 PLC 联调 profile；FLOAT32 是本机模拟约定，不得当成真实 Easy521 字序、权限或动作语义。 |

构建器只接受这两个值；未知 profile 会直接拒绝。无论选择哪种 profile，制品中都只保留一份 `web/assets/points.json`，不会把 `points.simulatorFloat32.json` 源文件一并发布。

## 3. 新 clone 可复现构建（Windows x64）

新 clone 不需要预装 Go、Node、TypeScript，也不得借用 `.tools`、历史 `.cache`
或历史 `GOMODCACHE`。它仍需要 Windows PowerShell、Git 命令行与 Git for Windows（`bash`、
`cygpath`、`file`、`grep`）。唯一正式入口是：

```powershell
# 在仓库根目录执行
.\tools\build-release.ps1 -Version <approved-unique-version>
```

Git for Windows 不需要安装在固定目录：脚本先由 PATH 中的 `git.exe` 推导其安装根；无法发现时可传
`-GitBash` 或设置 `BLOCK_GIT_BASH`，两者均须指向该 Git for Windows 安装内的 `bash.exe`。

可为一次独立验证指定一个新的 state root：

```powershell
.\tools\build-release.ps1 -Version <approved-unique-version> `
  -StateRoot '.cache\block-clean-build'
```

正式构建默认拒绝已修改或未跟踪的工作树，并把 commit、Git tree、clean 状态写入
`artifact/build-metadata.json`。`-AllowDirty` 仅限开发诊断；归档最终 hash 必须在提交后由
干净树重新构建。state root 只能是仓库根目录 `.cache` 的直接子目录，脚本拒绝卷根、仓库根、
junction 和没有 owner marker 的非空目录；`-FreshState` 也只会删除本工具已拥有的 state root。

入口在该 state root 内完成以下步骤：

1. 从 `go.dev` 与 `nodejs.org` 下载 Go `1.26.5`、Node.js `24.14.0` 的 Windows x64 ZIP，并用脚本中固定的 SHA-256 校验；不会使用系统 PATH 的同名工具。
2. 只从根 `package-lock.json` 安装精确的 TypeScript `5.6.3` 到 state root；从 `apps/block-hmi/assets/hmi.mts` 和 `tools/plc-simulator/web/app.ts` 重编译到 state root，逐字节比对已跟踪的 `hmi.mjs`、`app.js`，不同即失败且不会覆写运行时资产。
3. 在 state root 的全新 `GOMODCACHE` 中按每个 `go.mod`/`go.sum` 下载模块并执行 `go mod verify`。默认下载链是 `https://goproxy.cn|https://proxy.golang.org|direct`，`|` 允许网络错误时继续 fallback；它只是可配置的可用性策略，并不保证任何镜像永远可达。每个模块仍由 `go.mod`/`go.sum` 与 `sum.golang.org` 校验；随后设 `GOPROXY=off`、`GOSUMDB=off`。
4. 运行 HMI Node 测试、三个 Go module 的 `go test` 和 Agent `go vet`，再以 `GOENV=off`、`GOWORK=off`、`GO111MODULE=on`、空 `GOROOT`、`GOTOOLCHAIN=local`、`CGO_ENABLED=0`、`GOOS=linux`、`GOARCH=arm64`、`GOARM64=v8.0`、`-mod=readonly`、`-trimpath`、`-buildvcs=false` 构建 `artifact/bin/block-agent`。脚本内部调用 Git Bash，并校验输出为 AArch64 ELF。

所有 `TEMP`、`TMP`、`TMPDIR`、`GOTMPDIR`、`GOPATH`、`GOCACHE`、`GOMODCACHE`、npm cache、工具链与制品均在 state root 中。脚本固定 `GOENV=off`、`GO111MODULE=on`、清空 `GOROOT`，并清除 `GOPRIVATE`、`GONOPROXY`、`GONOSUMDB`、`GOINSECURE`、`NODE_OPTIONS`、`NODE_PATH`、Node TLS 覆盖、继承的 `NPM_CONFIG_*` 和全部 `GIT_CONFIG_*` 注入；不会输出这些继承值。`HTTP(S)_PROXY` 与正常 Git 代理仅可影响传输路径，值不记录且不替代内容校验。正式 `build-release.ps1` 不依赖 Common；仅可选 SSH Bootstrap 验证需要另行 clone Common 并显式传入 `-CommonRoot`。完成发布或验证后，只能删除已明确核对的该次 state root。默认 profile 为正式 `points.json`；需要 legacy 电脑模拟点表时显式加 `-PLCProfile simulatorFloat32`。

## 5. 制品校验和可选打包

第 3 节已经验证 AArch64 ELF。需要在提交给 Release 前额外盘点或打包时，使用刚才
明确指定的 state root，而不是任何历史任务缓存。先在 PowerShell 对点表做结构计数：

```powershell
$StateRoot = Join-Path (Get-Location) '.cache\block-clean-build'
$ArtifactDir = Join-Path $StateRoot 'artifact'
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

预期：`default` 为 `Total=42`、`Bool=33`、`Float32=1`、`Int16=5`、`Int32=3`、`Bindings=54`、`ScanIntervalMs=500`；其中 `Bool=33` 包含 15 个报警 BOOL。仅显式选择的 legacy `simulatorFloat32` 保持 `Total=30`、`Bool=8`、`Float32=22`，且 `SimulatorSourceLeaked=False`。

若要生成可传输的 POSIX tar 包，Windows `tar.exe` 可能丢失脚本执行位；此时在
**WSL/Linux shell** 使用同一个 state root 生成 manifest 和唯一压缩包。这是可选的
打包步骤，不是第 3 节新 clone 构建的前提：

```bash
STATE_ROOT="$PWD/.cache/block-clean-build"
ARTIFACT_DIR="$STATE_ROOT/artifact"
VERSION='<approved-unique-version>'

file "$ARTIFACT_DIR/bin/block-agent" | grep -Eq 'ELF .*ARM aarch64'
test "$(cat "$ARTIFACT_DIR/VERSION")" = "$VERSION"

(
  cd "$ARTIFACT_DIR"
  find . -type f -print0 | sort -z | xargs -0 sha256sum
) > "$STATE_ROOT/artifact.sha256"

cd "$STATE_ROOT"
tar --format=posix -czf artifact.tar.gz artifact artifact.sha256
tar -tzf artifact.tar.gz >/dev/null
sha256sum artifact.tar.gz artifact.sha256 artifact/bin/block-agent
```

清理必须先 dry-run，且只在用户终端确认清单后执行；该脚本只处理 owner marker 管理的 state root 中
的精确缓存、完整 `artifact/` 发布目录和 tar/manifest，不删除源码、README、配置、SSH 材料或现场证据：

```powershell
.\tools\cleanup-build-artifacts.ps1 -StateRoot '.cache\block-clean-build'
.\tools\cleanup-build-artifacts.ps1 -StateRoot '.cache\block-clean-build' -Execute
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

### Wi-Fi DHCP、连接发现与 SSH 失败处置（已验证）

设备的 Wi-Fi 使用 DHCP，`wlan0` 的 IPv4 地址可能变化；不得硬编码或假定它永远是 `192.168.0.104`。需要通过 Wi-Fi 连接前，先从已获授权的路由器 DHCP 租约或维护页确认当前 `wlan0` 地址；也可将网线接到登记管理地址 `192.168.1.104`，按 HTTPS bootstrap 获取短期 SSH，再在设备上查询当前 `wlan0` 地址。不要把登记的有线管理地址当作 Wi-Fi 地址，或反过来替代它。

已验证 Wi-Fi 可以正常关联 `SFLK`，且默认路由和网关通信正常。此前仅用 Wi-Fi 时 HTTPS SSH 申请失败，不表示 Wi-Fi 断连：bootstrap 的 `connection.json` 与 host-key 扫描固定返回受信的有线管理地址 `192.168.1.104`；拔掉网线后，客户端无法扫描该地址，因此 bootstrap 失败。

网络、HTTPS bootstrap 或严格 SSH 任一步失败时立即停止。不得禁用校验、接受未知 host key，或使用代理/绕路连接。需要恢复维护访问时，只可先接网线到登记管理地址，再按 HTTPS 获取短期 SSH 并继续使用已固定的 host key 校验。

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
$Archive = Join-Path (Get-Location) ".cache\block-release-$Version\artifact.tar.gz"

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
