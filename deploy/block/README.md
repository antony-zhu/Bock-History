# Block v2 部署

本目录部署一个 Block Go 进程和一个 Chromium Kiosk 进程：

- block.service：唯一的 Go 服务。它在 127.0.0.1:8080 提供静态页面、HTTP 健康检查和 WebSocket；在 0.0.0.0:8443 只提供 HTTPS 维护接口。
- block-kiosk.service：等待本地健康检查成功后打开 http://127.0.0.1:8080/。
- ssh.service：Ubuntu 系统服务继续监听 22；本部署不安装或替换 SSH 服务。

不安装旧的 block-hmi、block-agent 或 PLC 模拟器 systemd 服务。SQLite 由
block.service 直接使用，不是独立服务。

## 运行时命令接缝

当前已知的 Go 命令由 systemd 的环境文件提供参数：

~~~
/opt/block/current/bin/block-agent \
  -local-http-address 127.0.0.1:8080 \
  -state-db /var/lib/block/block.db \
  -hmi-static-dir /opt/block/current/web
~~~

完整 flag 值保存在 /etc/block/block.env，并由 systemd/block.service 的
EnvironmentFile 读取。后续统一运行时若改名或改参数，只改这一处及
install.sh 对发布产物的检查；不要通过增加第二个服务或兼容包装器来并行运行
旧架构。

## 配置与前端资源

config/block.env.example 只包含身份、路径、监听地址、维护 HTTPS 与 MQTTS
连接默认项。它没有 points 变量，安装脚本也会拒绝点位变量。点位表和页面
布局由发布产物中的 web/assets/points.json 提供；用户建立本地 HMI 会话后，
前端把完整点位表发送给 Block。Block 重启或 WebSocket 断开后不从配置或
SQLite 恢复点位和扫描计划。

默认 mqtt.enabled 为 false，所以没有 BDM 或 Wi-Fi 时 Block 仍可启动，并
保持空闲的本地 HTTP/WS 和维护 HTTPS；PLC 和 MQTTS 只在有效的本地 HMI
会话中运行。

维护 HTTPS 证书和私钥仅以路径出现在示例中。由设备管理员单独放入
/etc/block/certs；本目录不生成、不复制、不提交私钥或密码。

## 构建与安装

在 Block 源码根目录构建一个不可变发布目录：

~~~
deploy/block/build.sh --output /opt/block/uploads/block-1.0.0 --version 1.0.0
~~~

该目录必须有以下布局：

~~~
bin/block-agent
web/index.html
web/assets/points.json
VERSION
~~~

设备管理员按现场身份、PLC 与证书路径复制并检查配置后执行：

~~~
sudo deploy/block/install.sh --execute \
  --artifact-dir /opt/block/uploads/block-1.0.0 \
  --config /path/to/block.env \
  --version 1.0.0
~~~

安装创建 /opt/block/releases/1.0.0 并原子切换 /opt/block/current 链接，然后
启动 block.service、检查 /healthz，最后启动 Kiosk。若健康检查失败，脚本
停止并保留发布目录以便诊断；它不会擅自执行复杂的自动回滚。

## 日常检查与回滚

~~~
/opt/block/current/deploy/version.sh
/opt/block/current/deploy/verify-install.sh --expected-version 1.0.0
sudo /opt/block/current/deploy/rollback.sh --execute
~~~

回滚只切换到安装前记录的不可变发布目录，重启 Block 和 Kiosk，并保留
/etc/block 配置及 /var/lib/block SQLite 数据。

## 网络边界

本机业务 HTTP/WS 仅为 127.0.0.1:8080。外部维护仅为 TLS 的 8443，SSH 保持
22。设备管理员必须在主机防火墙上按指定有线网卡与来源网段限制 8443 和 22；
不得为了部署或测试开放 8080、明文 HTTP/WS 或 MQTT 1883。
