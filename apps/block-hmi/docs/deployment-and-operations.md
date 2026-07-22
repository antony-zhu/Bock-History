# 部署与运维说明

本文只描述单台 Block 的本地 HMI 服务部署。当前阶段一台 Block 对应一台设备；Block 必须脱离路由器、BDM 和 Android Pad 独立运行。本服务不是 BDM/Pad API。

> **TLS 实施门禁**：当前 Go 原型和 `deploy/block-hmi.service` 只支持明文 HTTP，不符合 ADR-003，只能在隔离开发环境使用。正式部署前必须实现受信 HTTPS `127.0.0.1:8443`，同机 HMI→Agent 使用 Unix socket 或 HTTPS/mTLS，关闭 `8080/8081`，并拒绝明文请求且不做跳转。调试期 SSH `22/tcp` 保留现状。

## 1. 运行形态

`main.go` 将 `index.html` 和 `assets/` 嵌入 Go 可执行文件，并同时提供静态页面、`/healthz` 和 `/api/v1/*`。当前实现使用明文 HTTP。生产版本必须改为以下二选一：应用原生 HTTPS；或 Nginx/Caddy 在 HTTPS `8443` 终止 TLS、再通过受文件权限保护的 Unix socket 访问应用。禁止 HTTPS 反向代理再通过 loopback 明文 TCP 转发。

当前 Controller 是演示实现，不连接真实 PLC。配置 `BLOCK_HMI_DATA_FILE` 后，每次成功写操作都会把完整演示状态和审计记录写入数据文件；状态变化和指令执行仍用于联调演示。

## 2. 构建与测试

```bash
go test ./...
go build -trimpath -ldflags="-s -w" -o block-hmi-server .
```

交叉构建 ARM64 Linux：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -trimpath -ldflags="-s -w" -o block-hmi-server .
```

发布前至少验证：

- `go test ./...` 通过；
- 页面五个导航、五套皮肤、横屏适配和软键盘正常；
- `/healthz` 返回 `200`；
- `/api/v1/state` 返回合法 JSON 和 `ETag`；
- 参数、命令、报警确认成功后会写审计；当前演示控制器尚未记录失败操作，生产 Driver 接入前必须补齐；
- 重启后维护参数和审计仍存在；
- 数据文件损坏、只读或磁盘空间不足时服务不会静默丢数据。

## 3. 当前原型环境变量

下表只描述现有 HTTP 原型，不能直接用于生产。TLS 任务应新增证书/私钥或 Unix socket 配置，并在代码、测试和 systemd 模板中同时实现。

| 变量 | 示例 | 说明 |
| --- | --- | --- |
| `BLOCK_HMI_ADDR` | `127.0.0.1:8080` | 当前原型 HTTP 地址；生产必须被 HTTPS `8443` 或 Unix socket 取代 |
| `BLOCK_HMI_BASE_PATH` | `/block-apple-style` | 公开子路径；默认即为 `/block-apple-style`，设为 `/` 可只使用根路径 |
| `BLOCK_HMI_DATA_FILE` | `/var/lib/block-hmi/state.json` | 演示状态和审计持久化文件；留空则只存内存 |

生产数据目录应独立于二进制目录，归服务账号所有，并仅授予服务账号读写权限。例如：

```bash
install -d -o www-data -g www-data -m 0750 /var/lib/block-hmi
```

不要将数据文件放在 `/opt/block-hmi/bin/`，否则更新二进制时容易误覆盖生产数据。

## 4. systemd

仓库模板 `deploy/block-hmi.service` 仍是 HTTP 原型，已标记为禁止生产使用。实现 TLS 后，正式模板至少需要显式声明 HTTPS 地址、受信证书和私钥路径，或改为 Unix socket。以下是目标配置形态，变量名称需要在代码任务中实现后才能使用：

```ini
[Service]
Environment=BLOCK_HMI_ADDR=127.0.0.1:8443
Environment=BLOCK_HMI_TLS_CERT=/etc/block/certs/block-hmi.crt
Environment=BLOCK_HMI_TLS_KEY=/etc/block/certs/block-hmi.key
Environment=BLOCK_HMI_BASE_PATH=/block-apple-style
Environment=BLOCK_HMI_DATA_FILE=/var/lib/block-hmi/state.json
ReadOnlyPaths=/etc/block/certs
ReadWritePaths=/var/lib/block-hmi
```

推荐发布流程：

```bash
install -m 0755 block-hmi-server /opt/block-hmi/bin/block-hmi-server.new
mv /opt/block-hmi/bin/block-hmi-server.new /opt/block-hmi/bin/current
systemctl daemon-reload
systemctl restart block-hmi
systemctl is-active block-hmi
curl --fail --silent --show-error \
  --cacert /etc/block/certs/ca.crt \
  https://127.0.0.1:8443/healthz
```

禁止使用 `curl -k` 或 `--insecure`。若证书 SAN 不包含 `127.0.0.1`，健康检查必须使用证书内的稳定 DNS 名。

替换二进制和数据文件写入都应保持同一文件系统内的原子重命名。发布脚本还应在覆盖 `current` 前保存上一版，以便快速回滚。

## 5. 反向代理

Block HMI 优先由应用原生提供 HTTPS。若必须使用反向代理，浏览器到代理必须为 HTTPS，代理到应用必须走 Unix socket；不得使用明文 loopback TCP。示意配置中的 `proxy_pass http://block_hmi_socket` 只表示 Unix socket 上的 HTTP 报文语义，不建立未加密 TCP 连接：

```nginx
upstream block_hmi_socket {
    server unix:/run/block/block-hmi.sock;
}

server {
    listen 127.0.0.1:8443 ssl;
    ssl_certificate /etc/block/certs/block-hmi.crt;
    ssl_certificate_key /etc/block/certs/block-hmi.key;

location /block-apple-style/ {
    proxy_pass http://block_hmi_socket/;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto https;
}
}
```

不得增加 `listen 80` 或 HTTP→HTTPS 重定向。明文请求应在没有监听器时连接失败，或被防火墙直接拒绝/丢弃。

上线认证后，还需要配置安全 Cookie、CSRF 防护、登录限流和合适的 Content Security Policy；当前版本未实现这些认证能力。

## 6. 数据文件与备份

- 服务通过临时文件加原子重命名更新 `BLOCK_HMI_DATA_FILE`；数据目录必须与临时文件位于同一文件系统。
- 备份时复制完整文件，不要直接编辑运行中的数据文件。
- 恢复前停止服务，校验 JSON 和文件权限，再替换数据文件并启动服务。
- 备份应加密并限制访问，因为审计中包含操作员标识和生产操作记录。
- 保留期、异地备份和恢复演练由工厂的数据治理要求决定。

## 7. 监控与故障处理

建议监控：

- 进程存活、重启次数和 `/healthz`；`/healthz` 只是进程存活检查，不代表 PLC 已连接；
- API 5xx、响应时间和 `backend_unavailable` 数量；接入 PLC Driver 后再监控其连接/超时错误码；
- PLC 最后成功通信时间与状态快照时间；
- 数据文件大小、写入错误、磁盘空间和最近备份时间；
- 审计记录连续性和失败操作比例。

常见处理：

| 现象 | 检查 | 处理 |
| --- | --- | --- |
| 页面可开但数据不刷新 | 浏览器网络面板、`/api/v1/state`、反向代理路径 | 修复代理或 API 服务；前端保持离线提示 |
| 保存参数返回 `409` | 当前 `revision` | 刷新状态，让操作员重新确认 |
| 写操作返回 `operator_required` | `X-Operator` 是否发送 | 当前联调环境补充操作员；生产环境应转向真实会话身份 |
| 重启后参数丢失 | `BLOCK_HMI_DATA_FILE`、目录权限、systemd `ReadWritePaths` | 修复持久化路径并从备份恢复 |
| PLC 超时 | 网络、Driver 日志、PLC 运行/联锁状态 | 先读取状态，不重复发送危险命令 |
| 手机打开旧页面 | HTML/CSS 缓存和代理缓存头 | 清理缓存并确认入口文件版本；不要对入口 HTML 做长期强缓存 |

## 8. 回滚

1. 保存当前二进制、数据文件备份及对应版本号。
2. 将 `/opt/block-hmi/bin/current` 原子替换为上一版二进制。
3. 仅当数据格式不兼容时停止服务并恢复匹配的数据文件；不要用旧程序直接写入未知的新格式。
4. 启动服务，检查 `/healthz`、`/api/v1/state` 和只读页面。
5. 在确认配置和 PLC 环境安全后，再测试一条低风险写操作及其审计记录。
