# AGENTS.md — AirType 开发指南

微信/QQ 二选一的"隔空打字"Windows 托盘工具：手机发消息给 Bot，PC 当前焦点窗口实时"打"出这段字。Go 单文件 EXE（管理员 manifest），walk 原生 UI。

## 目录与分层

- `cmd/airtype` — 产品入口（托盘常驻，`-H windowsgui`）
- `internal/bot` — 微信通道（iLink 长轮询，the-yex/wechat-ilink-sdk）；`internal/qqbot` — QQ 通道（官方机器人平台，WebSocket）。**两通道接口形态对齐**（OnText/OnState/LastReceivedAt），互斥二选一
- `internal/typer` — SendInput 注入；`internal/ui` — 托盘/历史弹窗/二维码窗口/通道选择窗口（walk）
- `internal/win` — Win32 直调（焦点/DWM/单实例/托盘守护/原生菜单）
- `internal/history|settings|applog|paths|autostart` — 存储与配套
- `cmd/{selftest,typetest-target,traytest,qqtest}` — 测试工具；`winres/` — manifest/图标源；`assets/icons/` — 图标 SVG/PNG 源

**层规则**：通道包只发 `OnText(text)`，注入决策全在 `cmd/airtype`（暂停/弹窗打开/前台是桌面时不注入）；UI 与业务只经 `ui.Config` 回调解耦。改通道行为先读 `docs/开发方案.md` §10。

## 构建与测试

```bash
export PATH="/c/Program Files/Go/bin:$PATH"   # Go 在这里，不在默认 PATH
gofmt -w <改动的文件> && go vet ./... && go test ./...
CGO_ENABLED=0 go build -trimpath -ldflags "-H windowsgui -s -w" -o build/ ./cmd/...
```

- 冒烟：`build/traytest.exe`（约 22s 跑完，末尾"全部测试序列执行完毕"、零 ERROR 即过；在临时目录跑避免残留 traytest-history.json，跑完 taskkill）
- QQ 网关联调：`build/qqtest.exe -appid <id> -secret <s> [-seconds 30]`
- 资源（图标/manifest）改 `winres/*.json` 后须 `go-winres make --arch amd64 --out cmd/<目录>/rsrc`；`.syso` 已入库

## 约定

- 注释、commit message、UI 文案均中文；提交信息用**文件方式**（`git commit -F .git/COMMIT_MSG`，文件用工具写 UTF-8）——shell 直接 `-m` 中文会乱码
- push 直连常失败，重试加代理：`git -c http.proxy=http://127.0.0.1:7890 push origin main`
- Windows 专属文件都带 `//go:build windows`；`internal/qqbot` 的凭据文件 `qqbot.json`、微信 token `default.json` 已 gitignore，**绝不入库**
- 单实例锁：`win.AcquireSingleInstance("AirType")` 失败时先 `win.WakeRunningInstance()` 再退出

## 平台踩坑（都是真金白银调出来的）

- **INPUT 结构 x64 下是 40 字节**（union 按 MOUSEINPUT=32 补齐），布局错则 SendInput 因 cbSize 整体拒绝；`typer_test.go` 有断言锁定
- **walk**：带子控件的窗口必须设 Layout（否则 startLayout 崩）；`DPI()` 必须在 `Show()` 之后取；`Synchronize` 只入队不唤醒；托盘图标丢了只能**销毁重建** NotifyIcon（SetVisible 是 NIM_MODIFY 救不回），`ui.Tray.recreateNotifyIcon`；walk 菜单强制 `MNS_CHECKORBMP`（左侧空白列是它，非 bug）；walk 公开 API 先查源码（`~/go/pkg/mod/github.com/lxn/walk@*/`），别凭记忆
- **windowsgui**：stderr 是死句柄，日志必须走 `applog`（tolerantWriter）
- **QQ 协议**：`getAppAccessToken` 返回的 `expires_in` 是**字符串**；绑定密文布局 iv(12)+ct+tag(16) AES-256-GCM；WS intents=1<<25；扫码绑定协议细节见 `internal/qqbot/bind.go` 头注释
- **日志 age 字段**包含本机时钟偏差（曾因快 8s 误判恒定延迟）；QQ（WebSocket 推送）天生比微信（长轮询）快，属平台差异
- 二次实例/任务栏重建的图标守护链路在 `internal/win/trayguard.go`（子类化主窗口），改动 tray.go 的窗口创建时注意它依赖 mw 句柄

## 文档

- `README.md` — 中英双语，改用户可见行为（菜单/流程/延迟口径）时两处同步
- `docs/开发方案.md` — 设计与决策记录，§10 是 v1.2（QQ 通道/自动回车/图标守护）的增量演进，改通道语义前必读
