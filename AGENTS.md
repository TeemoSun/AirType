# AGENTS.md — AirType 开发指南

微信/QQ 二选一的"隔空打字"Windows 托盘工具：手机发消息给 Bot，PC 当前焦点窗口实时"打"出这段字。Go 单文件 EXE（**asInvoker**，无需管理员；仅注入提权窗口时受 UIPI 限制并明确提示），walk 原生 UI。

## 目录与分层

- `cmd/airtype` — 产品入口（托盘常驻，`-H windowsgui`）
- `internal/bot` — 微信通道（iLink 长轮询，the-yex/wechat-ilink-sdk；token 经 DPAPI 加密存储于 `store.go`）；`internal/qqbot` — QQ 通道（官方机器人平台，WebSocket + RESUME 会话恢复）。**两通道接口形态对齐**（OnText/OnState/LastReceivedAt），互斥二选一
- `internal/typer` — SendInput 注入；`internal/ui` — 托盘/菜单（tray.go+tray_menu.go）/扫码窗（tray_qr.go）/通道选择窗（tray_chooser.go）/测试钩子（tray_hooks.go）/历史弹窗（popup.go）/主题（theme.go）
- `internal/win` — Win32 直调（焦点/DWM/单实例/托盘守护/原生菜单 menu.go/**DirectWrite 文本渲染 dwrite.go**）
- `internal/secret` — Windows DPAPI 加解密（非 Windows 提供直通桩）
- `internal/history|settings|applog|paths|autostart` — 存储与配套
- `cmd/{selftest,typetest-target,traytest,qqtest}` — 测试工具；`cmd/icongen` — 图标生成器（产物在 `internal/ui/icons/`，已入库）；`cmd/uishot` — UI 截图工具（改 UI 后自检，本工具不入库）；`winres/` — manifest/图标源；`scripts/build.sh` — 统一构建入口

**层规则**：通道包只发 `OnText(text)`，注入决策全在 `cmd/airtype`（暂停/弹窗打开/前台是桌面时不注入）；UI 与业务只经 `ui.Config` 回调解耦。改通道行为先读 `docs/开发方案.md` §10。

## 构建与测试

```bash
export PATH="/c/Program Files/Go/bin:$PATH"   # Go 在这里，不在默认 PATH
scripts/build.sh all        # vet + test + 全部二进制
gofmt -w <改动的文件>        # CI 校验 gofmt/tidy/vet/test
```

- UI 冒烟：`build/traytest.exe`（约 25s，末尾"全部测试序列执行完毕"、零 ERROR 即过；它会常驻等手动退出，跑完 `taskkill //IM traytest.exe //F`，在临时目录跑避免残留文件）
- UI 截图自检：`scripts/build.sh uishot`（真实渲染六张图到 temp；读 PNG 后用视觉模型核对新样式）
- QQ 网关：协议本身有 httptest 单测（`internal/qqbot/gateway_test.go`）；真机联调才用 `build/qqtest.exe`
- 图标：改设计改 `cmd/icongen/main.go` 后 `scripts/build.sh icons`（四态×四尺寸全部重新生成入库）
- 资源（manifest）：`scripts/build.sh winres`；`.syso` 开发版已入库，**Release 的 PE 版本号由 CI 按 tag 重算**（本地不用管）

## 约定

- 注释、commit message、UI 文案均中文；提交信息用**文件方式**（`git commit -F .git/COMMIT_MSG`，文件用工具写 UTF-8）——shell 直接 `-m` 中文会乱码
- push 直连常失败，重试加代理：`git -c http.proxy=http://127.0.0.1:7890 push origin main`；`go mod tidy` 挂网络时加 `GOPROXY=off`（全在本地缓存）
- 错误气泡只给人话（`friendlyError`），原始错误进日志；不得把 `err.Error()` 直接拼进气泡
- 凭据类文件（`qqbot.json`、`default.json`、任何 secret）绝不入库；日志不落完整 openid（用短哈希）
- Windows 专属文件带 `//go:build windows`；`internal/qqbot` 纯网络代码**无**平台 tag（可在任意平台跑测试），`cmd/qqtest` 保留 tag
- 单实例锁：`win.AcquireSingleInstance("AirType")` 失败时先 `win.WakeRunningInstance()` 再退出

## 平台踩坑（都是真金白银调出来的）

- **INPUT 结构 x64 下是 40 字节**（union 按 MOUSEINPUT=32 补齐），布局错则 SendInput 因 cbSize 整体拒绝；`typer_test.go` 有断言锁定
- **walk**：带子控件的窗口必须设 Layout（否则 startLayout 崩）；`DPI()` 必须在 `Show()` 之后取；`Synchronize` 只入队不唤醒；托盘图标丢了只能**销毁重建** NotifyIcon（SetVisible 是 NIM_MODIFY 救不回）；walk 菜单强制 `MNS_CHECKORBMP`（左侧空白列是它，非 bug）；**walk Label 按单行测高**——长说明文案会被无声截断，必须拆成多个 Label；**NewBitmapFromImageForDPI 语义是"源图定义在 96dpi"**——按物理像素生成的位图要 ForDPI 包装，否则双重折算（二维码曾因此只剩一半）；walk 公开 API 先查源码（`~/go/pkg/mod/github.com/lxn/walk@*/`），别凭记忆
- **COM/Go syscall**：COM 对象指针指向的是 **vtable 指针**，取方法要两次解引用（`dwrite.go` 的 vtCall；直接 `*(obj+index*8)` 必崩）；vtable 槽位以 Wine 的 d2d1.idl/dwrite.idl 为权威；COM 指针一律存 `unsafe.Pointer`（go vet 禁止 uintptr→unsafe.Pointer）；数组索引式取槽位是 vet 认可写法
- **windowsgui**：stderr 是死句柄，日志必须走 `applog`（tolerantWriter）
- **QQ 协议**：`getAppAccessToken` 的 `expires_in` 是**字符串**；绑定密文布局 iv(12)+ct+tag(16) AES-256-GCM；WS intents=1<<25；WS 读超时必须**每帧续期**（gorilla 的 deadline 是绝对时刻，不续期则固定时长必断）；C2C 消息必须按绑定者 openid 过滤；细节见 `internal/qqbot/bind.go` 头注释与 §10.6
- **日志 age 字段**包含本机时钟偏差（曾因快 8s 误判恒定延迟）；QQ（WebSocket 推送）天生比微信（长轮询）快，属平台差异
- 二次实例/任务栏重建的图标守护链路在 `internal/win/trayguard.go`（子类化主窗口），改动 tray.go 的窗口创建时注意它依赖 mw 句柄
- 图标生成与主题配色改动后，用 `uishot` 截图 + 视觉模型验收，别只看代码

## 文档

- `README.md` — 中英双语，改用户可见行为（菜单/流程/状态语义）时两处同步
- `docs/开发方案.md` — 设计与决策记录：§10 是 v1.2 增量演进；**§10.5/§10.6 是历史弹窗定稿与全面质量版本**（本次协议修复/DPAPI/UI 重构/DirectWrite 的完整决策记录），改相关模块前必读
- `CONTRIBUTING.md` — 面向外部贡献者的精简版
