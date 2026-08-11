# PLC 模拟器

这是用于本机 Block PLC 协议联调的 Windows 友好型 Modbus TCP 模拟器。它保留
原有的 Modbus 行为：固定 Unit 1（可改）、FC03 读取保持寄存器、FC22 掩码写入并
回显请求；FC06、FC16 和其他功能码一律返回 `Illegal Function (01)`。

现在程序还带有一个仅监听本机回环地址的管理页面。双击 `plc-simulator.exe` 会
启动 Modbus TCP 和页面，并在默认浏览器打开页面。页面可维护点位定义，查看机器
通过 FC22 造成的变化，并写入被标记为“允许人工写入”的点位。

## 双击和命令行

把 `plc-simulator.exe` 放到希望保存点位 JSON 的目录后双击即可。默认地址为：

- Modbus TCP：`127.0.0.1:1502`
- 管理页面：`http://127.0.0.1:15080`
- 点位定义：当前目录的 `plc-simulator-points.json`

命令行示例：

~~~text
plc-simulator.exe --listen 127.0.0.1:1502 --unit-id 1 --register 504=0x0000
~~~

直连设备联调时，按需将 Modbus 监听绑定到电脑网卡地址；管理页面仍只允许绑定
到回环地址：

~~~text
plc-simulator.exe --listen 192.168.x.x:1502 --ui-address 127.0.0.1:15080 --points-file C:\plc\points.json
~~~

常用参数：

- `--register ADDRESS=VALUE`：设置初始 16 位寄存器，可重复使用。
- `--ui-address`：管理页面地址，默认 `127.0.0.1:15080`，只接受回环地址。
- `--points-file`：点位 JSON 路径，默认当前目录的 `plc-simulator-points.json`。
- `--open-ui`：启动后打开默认浏览器（默认开启）。
- `--no-open-ui`：不自动打开浏览器。

页面仅用于本机开发工具，因此使用同源 loopback HTTP；它不对局域网提供管理端口。
按 `Ctrl+C` 可关闭 Modbus、管理页面和活动连接。

## 点位和 JSON

首次添加点位时会创建一个 JSON 数组。每一项包含后端生成且稳定的 `id`，例如：

~~~json
[
  {
    "id": "point-1760000000000000001",
    "name": "运行允许",
    "type": "bool",
    "description": "D504 的第 1 位",
    "address": "D504.1",
    "writable": true
  },
  {
    "id": "point-1760000000000000002",
    "name": "当前计数",
    "type": "uint16",
    "description": "",
    "address": "D504",
    "writable": false
  }
]
~~~

支持的类型仅有：

- `bool`：地址必须是 `D<number>.<0..15>`，例如 `D504.1`。
- `int16`：地址必须是 `D<number>`，页面按有符号 16 位数显示和写入。
- `uint16`：地址必须是 `D<number>`，页面按无符号 16 位数显示和写入。

`D` 编号范围是 `0` 到 `65535`。名称必须唯一；完全相同的类型和地址不能重复。
同一 D 字的整字视图和多个 bit 视图可以共存，并且共享同一个 16 位寄存器。

JSON 只保存点位定义。实时寄存器值只存在内存中，重启后会恢复为 `--register`
指定的初始值或零。删除或编辑点位定义不会清空或改写已有寄存器。

## 写入和实时变化

`writable: false` 只限制页面人工写入，机器仍可以通过 FC22 修改寄存器。
页面写 `bool` 时使用掩码写入，只改变目标 bit 并保留同一 D 字的相邻 bit；写
`int16` 或 `uint16` 时写入对应的完整 16 位字。

Modbus FC22 修改 D 字后，页面通过标准 SSE 立即刷新同一 D 字的所有点位视图。
SSE 断开后的重连由浏览器原生 `EventSource` 处理，页面会读取当前值，不保存或
补传寄存器历史。

每次有效 Modbus 请求仍会向标准输出输出一行 JSON trace，包含时间、对端、事务、
功能码、地址、掩码和结果。

## 开发与测试

页面源码是原生 TypeScript、HTML 和 CSS。`web/app.js` 是已经提交并嵌入 exe 的
运行时资产，不能用工作区已有的 TypeScript、Go 或历史模块缓存覆盖。新 clone 的正式
编译/验证统一从仓库根目录执行 `tools/build-release.ps1`：它在本次 state root 下载并
校验固定 Go、Node 和锁定的 TypeScript，重编译 `web/app.ts` 后逐字节比对 `web/app.js`，
再在新 `GOMODCACHE` 中验证此模块依赖。

直接 `go test ./...` 只可用于已经由统一 bootstrap 准备好受管 state root 的局部诊断，
不是正式构建入口，也不能借用 `.tools`、系统 PATH 或历史缓存来证明可复现性。
`go test -race ./...` 会启用 cgo，另需本机 `gcc` 或 `clang`；缺少 C 编译器时应明确跳过
该并发诊断，不能把普通测试通过写成 race 结果。
