# 贡献指南

感谢关注 AirType！这是一个 Windows 托盘"隔空打字"工具：手机给机器人发消息，电脑当前焦点窗口自动打字。

## 开发环境

- Windows 10/11 + [Go 1.27+](https://go.dev/dl/)
- Git Bash（运行 `scripts/build.sh` 用）
- 无需 CGO、无需管理员权限

## 常用命令

```bash
scripts/build.sh test     # vet + 单元测试
scripts/build.sh bin      # 全部二进制 → build/
scripts/build.sh icons    # 重新生成托盘图标（cmd/icongen）
scripts/build.sh uishot   # 拉起真实 UI 截图，改界面后自查观感
scripts/build.sh winres   # 重新生成图标/manifest 资源（.syso）
```

改了托盘/弹窗相关代码后，建议手工跑一次 UI 冒烟：`build/traytest.exe`（约 25 秒，观察输出中的 ✓）。

## 约定

- **注释与 commit 用中文**；commit 信息经 UTF-8 文件提交避免乱码：
  `git commit -F .git/COMMIT_MSG`
- `gofmt` 格式化、`go vet` 零告警是 CI 门槛
- `go.mod`/`go.sum` 保持 tidy 状态（CI 校验）
- 凭据类文件（`qqbot.json`、`default.json`）绝不入库
- 架构与踩坑记录见 [AGENTS.md](AGENTS.md) 与 [docs/开发方案.md](docs/开发方案.md)（中文）

## 提交 PR

1. Fork + 分支开发
2. 自测：`scripts/build.sh all`
3. PR 描述写清动机与影响面；涉及 UI 请附 `scripts/build.sh uishot` 产物截图

## 报 Bug

请用 issue 模板（bug report），附上 Windows 版本、AirType 版本（`-v`）与 `%LOCALAPPDATA%\AirType\airtype.log` 中的相关日志段。
