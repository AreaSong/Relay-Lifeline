# 贡献指南

[English](CONTRIBUTING.md)

## 开发环境

- Go 1.22 或更高版本
- Node.js 22 或更高版本
- Docker，用于镜像和集成检查

提交变更前运行完整门禁：

```bash
make check
```

部分新版 macOS/Xcode 组合可能让 Go 1.22 报告缺少 `LC_UUID`，此时使用外部链接：

```bash
go test -ldflags=-linkmode=external ./...
```

## 变更要求

- 按变更风险补充测试。
- 协议修改必须覆盖成功标记、错误事件、流中断、取消和敏感 Header 透传。
- 所有用户可见文字必须同时加入 `zh-CN` 和 `en-US` 词典。
- 稳定 JSON 字段、事件代码、消息代码和状态值保持英文。
- 不得在代码、测试、Issue、截图或提交中加入真实 API Key、提示词、响应或未脱敏上游错误。
- 没有明确迁移设计时，保持 `relay-lifeline` 技术标识兼容。

`npm run l10n:check` 会执行翻译门禁，并已加入 `make check`。详见[本地化说明](docs/localization.zh-CN.md)。
