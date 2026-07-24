# Block 智能体协作规则

本文件作用于整个 Block 仓库。

## 文件所有权

- `BLK-DEV` 负责 `services/block-agent/**`、`apps/block-hmi/**` 和 `deploy/block/**`。
- QA/集成角色只能在任务明确授权时修改 `tests/**`。
- 不修改 BDM 源码，不在本仓库实现 BDM Web 或 Android Pad。
- 不修改公共契约；跨组件变更先由 `ARCH-COMMON` 在
  `D:\codex\Block-DMP\repos\Common` 仓库完成。禁止访问迁移前的工作区外
  Common 路径。
- 两个智能体不得同时修改同一文件；跨所有权修改先提交接口变更请求。

## 产品边界

- 当前阶段一台 Block 对应一台设备。
- Block 在无 Wi-Fi、无 BDM、无 Pad 时必须正常完成本地采集、状态、HMI、报警、历史和允许的现场操作。
- HMI 只能调用 Block Local API，不得直连 BDM。
- HMI 不得绕过 `block-agent` 直接访问 PLC；PLC/设备控制器继续负责实时控制和安全联锁。
- BDM/Pad 第一阶段只读；唯一允许的 BDM→Block 通道是可靠补传所需的 `/down/sync`。
- 当前不实现企业服务器、MES、AGV、远程配置、远程命令、远程升级或远程告警确认。

## 通信与测试

- 同机通信使用 Unix socket 或 TLS；所有 TCP/IP 业务通信只能使用 HTTPS、MQTTS、WSS。
- 不部署、不监听、不跳转明文 `80/1883/8080/8081` 或 `ws://`。
- SSH `22/tcp` 仅为调试管理例外，不得由业务程序作为正式隧道。
- Block 变更必须包含“无 BDM/无 Wi-Fi”测试。
- 可靠消息必须具有稳定 `messageId`、序号、SQLite Outbox 和 BDM 数据库提交后的应用层确认。
- 禁止跳过证书校验；TLS 测试必须覆盖错误/过期证书、错误主机名和旧 TLS 版本。

## 仓库与设备安全

- 不读取、复制、提交或输出任何真实 `wifi.toml` 的值；只允许 `.example` 样例。
- 私钥、密码、真实 `.env`、证书私钥、现场配置、数据库、日志和目标机二进制不得进入仓库。
- IP/主机名不作为业务身份；使用 `siteId`、`blockId`、`deviceId`。
- 真实 `192.168.1.101` 只能由 `BLK-REL` 修改。其他角色只做本地开发和只读诊断。
