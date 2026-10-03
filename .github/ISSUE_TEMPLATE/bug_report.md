name: Bug 报告
description: AirType 使用中出了问题
labels: [bug]
body:
  - type: markdown
    attributes:
      value: |
        感谢反馈！请尽量填写以下信息，日志能极大加速定位。
  - type: input
    id: version
    attributes:
      label: AirType 版本
      description: 托盘菜单/命令行 `airtype -v`
      placeholder: v1.2.0
    validations:
      required: true
  - type: input
    id: os
    attributes:
      label: Windows 版本
      placeholder: Windows 11 24H2
    validations:
      required: true
  - type: dropdown
    id: channel
    attributes:
      label: 使用的通道
      options:
        - 微信
        - QQ 机器人
        - 都用了
  - type: textarea
    id: what
    attributes:
      label: 发生了什么？
      description: 预期行为 vs 实际行为
    validations:
      required: true
  - type: textarea
    id: log
    attributes:
      label: 相关日志
      description: |
        位置：%LOCALAPPDATA%\AirType\airtype.log（或托盘菜单"打开日志"）。
        请截取问题发生时间点前后的段落，注意不要泄露个人消息内容。
  - type: textarea
    id: repro
    attributes:
      label: 复现步骤（可选）
