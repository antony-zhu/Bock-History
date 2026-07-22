# Block 部署

本目录预留 Block 的非秘密配置样例、systemd、安装、健康检查、版本记录和回滚资料。

- [wifi.example.toml](wifi.example.toml) 只包含部署占位值。
- 真实 Wi-Fi、证书私钥、密码和现场配置不得进入 Git。
- 真实 `192.168.1.101` 只能由 `BLK-REL` 按受控发布流程修改。
- 正式业务端点只允许 Unix socket、HTTPS、MQTTS、WSS，不部署明文 `8080/8081`。
