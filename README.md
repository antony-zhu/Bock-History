# Block

Block 是面向单台设备、可独立交付和独立运行的边缘产品。当前阶段一台 Block 对应一台设备；本地采集、状态、HMI、报警、历史和允许的现场操作不得依赖 Wi-Fi、BDM 或 Android Pad。

## 仓库范围

```text
apps/block-hmi/                 # Block 本地 HMI；当前为 Go/HTML 演示基线
services/block-agent/           # 设备适配、状态、本地存储、Local API、Outbox
deploy/block/                   # Block 配置、systemd、安装与回滚资料
docs/requirements/              # Block 产品与 HMI 要求
docs/references/                # HMI 参考资料
tests/                          # 无 Wi-Fi / 无 BDM、单元与验收测试
archive/prototypes/web-hmi/     # 旧静态 HMI 原型，只读参考
```

本次仓库拆分只建立职责边界并迁移现有原型，没有实现新的 `block-agent` 业务代码。

## 公共架构与契约

公共架构和跨组件契约的唯一来源是相邻仓库 `D:/codex/Block-BDM-Common`。本仓库通过根目录的 `COMMON_BASELINE` 固定其确切 commit，不在本仓库复制一套可独立修改的公共契约。

修改 MQTT、OpenAPI、JSON Schema、身份字段或跨组件状态前，必须先由 `ARCH-COMMON` 更新 Common，再更新本仓库的 `COMMON_BASELINE`。

## 当前 HMI 基线

现有 [Block HMI](apps/block-hmi/README.md) 仍是演示 Controller，当前只实现明文 HTTP，不能作为满足 TLS-only 要求的生产发布物。正式实现必须：

- 只调用 Block Local API，不直连 BDM；
- 不绕过 `block-agent` 直接访问 PLC；
- 在无 Wi-Fi、无 BDM 时正常工作；
- 同机通信使用 Unix socket 或 TLS；
- 不部署明文 `8080/8081`。

## 安全

- 真实 `wifi.toml`、`.env`、密码、私钥、证书私钥和现场配置不得进入仓库。
- 只提交 [wifi.example.toml](deploy/block/wifi.example.toml) 这类明确占位样例。
- APK、数据库、日志、构建缓存和目标机二进制不得作为源码提交。
- 真实 `192.168.1.101` 只能由 `BLK-REL` 执行写操作；本仓库开发默认只在本地进行。
