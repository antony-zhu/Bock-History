# block-agent

本目录预留给 Block Runtime：设备适配、标准状态、报警、本地历史、SQLite Outbox、Block Local API 和可选 MQTTS 上行。

`REPO-SPLIT-001` 只建立目录职责，不实现业务代码。后续实现必须以 `COMMON_BASELINE` 指向的 Common 契约为准，并证明在无 Wi-Fi、无 BDM 时本地功能完整运行。
