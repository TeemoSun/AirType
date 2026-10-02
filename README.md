# AirType · 隔空打字

> 手机微信发一条消息，电脑在当前焦点输入框里立刻"打"出这段字。

Windows 绿色单文件 EXE（Go 实现）。常驻托盘运行，扫码绑定微信 Bot（iLink 协议 / 微信 ClawBot 接口），被动接收文字消息，通过 `SendInput` 实时注入当前焦点窗口。配合手机输入法的语音转文字，即可实现"对着手机说话，电脑上打字"。

📖 开发方案与计划见 [docs/开发方案.md](docs/开发方案.md)

## 使用方式

### v1.0 控制台版（已实现 ✅）

1. 运行 `airtype.exe`（需要管理员权限，用于向任意窗口注入键盘输入；UAC 拒绝则不运行）
2. 首次启动在终端显示二维码，用手机微信扫码绑定
3. 把电脑焦点切到任意输入框，用手机微信给 Bot 发文字消息，文字即刻出现在电脑上
4. token 持久化在 `%LOCALAPPDATA%\AirType\`，之后启动免扫码；日志在同目录 `airtype.log`
5. Ctrl+C 退出；`airtype -h` 查看选项，`-datadir` 可覆盖数据目录

> ⚠️ 消息中的换行以回车键注入：在微信 PC 版等"Enter 即发送"的聊天框里，多行文本会被逐行发送出去。

### v1.1 托盘常驻 / v2.0 历史弹窗（开发中 🚧）

- 常驻托盘图标（绿=已连接 / 灰=暂停 / 红=需扫码），无控制台窗口，扫码 GUI 化，暂停注入，开机自启
- OneDrive 风格历史弹窗：滚动查看收到的所有消息，单击某条重新注入到当前焦点输入框，右键可复制或删除

## 从源码构建

```bash
go mod tidy
go test ./...                          # 单元测试
go build -o build/ ./cmd/...           # 全部二进制（含注入自测工具）
```

注入链路自测（无人值守）：先启动 `build/typetest-target.exe`（测试靶子窗口），再运行
`build/selftest.exe -window "AirType Typetest Target" -text "hello 世界 😀"`，文本会注入靶子窗口并写入 `typetest-result.txt` 供比对。

资源（图标 / manifest）由 [go-winres](https://github.com/tc-hib/go-winres) 生成：修改 `winres/*.json` 后运行 `go-winres make --arch amd64 --out cmd/<目录>/rsrc`。

## 产品形态

- 常驻托盘图标（绿=已连接 / 灰=暂停 / 红=需扫码），无控制台窗口
- 新消息到达即注入 + 写入历史；"暂停注入"只记录不注入，消息不丢
- 历史弹窗：单击重发、右键复制/删除、失焦自动关闭

## 技术要点

- 消息通道：微信 ClawBot / iLink 协议（`ilinkai.weixin.qq.com`），长轮询接收，端到端延迟约 0.5~2 秒
- 只收不发：不回复任何消息，规避发送侧所有限制（context_token、频控、24 小时窗口）
- 键盘注入：Win32 `SendInput` + `KEYEVENTF_UNICODE`，中文直接注入、不依赖输入法
- 界面：`lxn/walk` 原生 Win32 控件（托盘 + 弹窗），焦点记忆与还原实现"点击历史条目打回原输入框"
- 单文件：`CGO_ENABLED=0` 静态编译，无 DLL 依赖，绿色免安装

## Roadmap

- **v1.0** 控制台 MVP：扫码绑定、实时注入、token 持久化 ✅
- **v1.1** 托盘常驻：托盘菜单、无控制台、扫码 GUI 化、暂停注入（开发中）
- **v2.0** 历史弹窗：OneDrive 风格弹窗、点击重发、复制/删除（规划）

## 免责声明

本项目是 iLink 协议的**独立客户端实现**，非微信官方工具，与腾讯无关联。仅供个人学习与效率工具用途；属于未授权客户端形态，使用产生的账号风险自负，请遵守《微信软件许可及服务协议》。

## License

[MIT](LICENSE)
