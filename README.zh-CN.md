# Relay-Lifeline（中转生命线）

[GitHub](https://github.com/AreaSong/Relay-Lifeline) | [English](README.md)

Relay-Lifeline 是面向 OpenAI-compatible API 中转站的本地可靠性网关。它位于 Codex、IDE 或其他 AI 客户端与现有中转站之间；上游返回任意错误时，它保持客户端连接，按配置等待后重新提交同一个请求。

本项目与 CLIProxyAPI（CPA）及任何模型提供商均无官方关联。

```text
AI 客户端
   │  沿用原 API Key，只修改 base_url
   ▼
Relay-Lifeline :8318
   │  Authorization 原样透传
   ▼
CLIProxyAPI :8317 或其他 OpenAI-compatible 中转站
   ▼
由中转站管理的账号、路由和模型提供商
```

公开项目名称统一为 **Relay-Lifeline**；二进制、镜像、环境变量和兼容 Header 使用与之对应的 `relay-lifeline` 技术标识。

## 核心能力

- 重试全部上游错误，或只重试临时错误。
- 默认在 60～120 秒之间随机等待。
- 支持无限重试或限制最大尝试次数。
- 可遵循上游 `Retry-After`。
- 校验 Responses API 与 Chat Completions SSE 完成标记。
- 缓存完整响应期间发送 SSE 保活注释。
- 完整响应缓存，避免半截输出和重复交付工具调用。
- 客户端取消传播、并发限制、等待队列和恢复削峰。
- 请求时间线、有界内存历史、诊断和风险提醒。
- 对结构化上游错误进行白名单提取、脱敏和长度限制。
- 可筛选、暂停刷新和下载的实时结构化运行日志，不包含请求或响应正文。
- 主动开启的临时诊断捕获：加密保存请求、每次 CPA 响应和最终响应，支持过滤预览、过滤下载与完整原文下载。
- 独立 Webhook 队列、事件过滤和投递重试。
- UI、管理 API、CLI、日志、诊断和 Webhook 全部支持中英文。
- UI、日志和通知语言独立配置并可热更新。
- 独立管理密钥和默认仅本机监听。

Relay-Lifeline 始终只连接一个中转站。账号池、供应商选择、模型映射、权重和多个中转站之间的故障切换仍由 CPA 或指定上游负责。

## 快速启动

```bash
cp config.docker.example.yaml config.docker.yaml
cp .env.example .env
```

在 `.env` 中设置足够长的随机 `RELAY_LIFELINE_ADMIN_KEY`，并生成独立的 32 字节捕获密钥：

```bash
openssl rand -base64 32
```

将结果写入 `RELAY_LIFELINE_CAPTURE_KEY`。然后修改 `config.docker.yaml`。CPA 位于宿主机 `8317` 时使用：

```yaml
upstream:
  base-url: "http://host.docker.internal:8317"
```

启动服务：

```bash
docker compose up -d --build
curl http://127.0.0.1:8318/healthz
```

管理控制台：<http://127.0.0.1:8318/admin/>。

AI 客户端只修改 Base URL，原 API Key 保持不变：

```toml
[model_providers.relay_lifeline]
base_url = "http://127.0.0.1:8318/v1"
wire_api = "responses"
```

两个服务处于同一个 Docker 网络时，也可以使用 CPA 服务名：

```yaml
upstream:
  base-url: "http://cli-proxy-api:8317"
```

## 重试语义

`all-errors` 会重试所有不成功的上游结果，包括 HTTP `4xx`、`5xx`、连接和超时错误、无效或空 JSON、`response.failed`、`response.incomplete` 以及截断的 SSE 流。

```yaml
retry:
  enabled: true
  mode: "all-errors"
  min-interval: "60s"
  max-interval: "120s"
  max-attempts: 0
  honor-retry-after: true
```

`max-attempts: 0` 表示无限重试。只有合法、完整的 `2xx` 响应才会结束重试。正常响应中的助手拒答不属于 API 错误。客户端断开或取消会终止当前上游调用和全部等待。

网关先缓存并校验上游响应，再向客户端交付，因此流中断时不会暴露半截正文。流式请求等待期间默认每 15 秒发送 SSE 注释；非流式 JSON 使用空白保活，不改变最终 JSON 值。

## 管理控制台

控制台使用独立的 `RELAY_LIFELINE_ADMIN_KEY`，可以：

- 查看活动请求、尝试次数、下次重试和安全失败详情。
- 查看请求时间线和有界内存历史。
- 查看长时间运行、尝试过多、鉴权错误、队列和磁盘风险。
- 执行不调用模型的一键诊断并导出脱敏 JSON 包。
- 暂停或恢复全部请求、立即重试或取消指定请求。
- 修改重试、流、队列、历史、风险、通知、日志和语言设置。
- 原子保存配置或从磁盘重新加载。
- 查看实时结构化日志；按级别、事件或请求 ID 筛选并下载。
- 临时捕获接下来的指定数量请求，查看过滤正文，或下载过滤/完整原文 ZIP。

历史只保存在内存中，重启后清空。监听地址、管理开关、上游传输参数、服务超时和日志级别需要重启；重试、队列、历史、风险、语言和通知设置会在运行期间读取最新配置。

## 双语配置

Web UI 可实时切换语言，并将选择保存到 `localStorage`。管理 API 请求发送 `Accept-Language`，响应返回 `Content-Language`。

```yaml
localization:
  default-locale: "zh-CN"
  fallback-locale: "en-US"

logging:
  locale: "zh-CN"

notifications:
  locale: "zh-CN"
```

稳定 JSON 字段、状态值、事件代码和消息代码保持英文，供程序可靠解析；面向人的文字再按语言翻译。贡献规范见[本地化说明](docs/localization.zh-CN.md)。

## 安全边界

- Docker 默认只发布 `127.0.0.1:8318`。
- 客户端 Authorization 只在内存中透传，不主动写入日志。
- 配置校验会拒绝记录请求体、响应体或 Authorization。
- 安全错误详情只包含白名单结构化字段，进入历史前完成脱敏。
- 临时响应文件权限为 `0600`，交付或失败后删除。
- 诊断导出会移除 URL 凭据、Query、Webhook 地址和错误详情。
- 管理控制台使用严格安全 Header，不依赖第三方 CDN。
- 临时捕获默认不活动；正文使用分块 AES-256-GCM 加密，认证 Header 永不落盘。
- 完整原文不能在线预览，只能在明确确认后流式解密到下载 ZIP，磁盘不生成明文 ZIP。
- 捕获默认保留 72 小时；空间或最低磁盘阈值不足时停止保存正文，不阻塞代理请求。
- `RELAY_LIFELINE_CAPTURE_KEY` 必须独立持久保存。第一版不维护历史密钥环，轮换前必须下载或清空旧捕获。

不要把管理端直接暴露到公网。需要远程访问时，应增加 TLS、访问控制和可信网络边界。

## 诊断与通知

诊断会检查配置、文件访问、管理密钥长度、CPA DNS/TCP 连通性、缓存权限和磁盘容量。上游检查只建立 TCP 连接，不发送模型请求，也不消耗 Token。

Webhook 可通知持续故障、恢复、长时间运行、尝试过多、鉴权错误、队列压力和磁盘压力。Payload 同时包含稳定的 `eventCode`、数值型 `elapsedSeconds` 和本地化文字。通知使用有界独立队列，不阻塞模型请求链路。

## 开发

要求 Go 1.22+、Node.js 22+，集成验证还需要 Docker。

```bash
make check
make docker-build
```

部分新版 macOS/Xcode 环境运行 Go 1.22 测试二进制时需要外部链接。如果内部链接器报告缺少 `LC_UUID`，执行 `go test -ldflags=-linkmode=external ./...`。

更多信息见[架构说明](docs/architecture.zh-CN.md)、[贡献指南](CONTRIBUTING.zh-CN.md)和[安全策略](SECURITY.zh-CN.md)。

## 回滚

Relay-Lifeline 不修改上游账号或 API Key。将客户端 `base_url` 改回 CPA 或原中转站即可立即绕过网关。升级已部署实例前，应保留旧镜像和旧配置文件。

## 已知风险

如果上游调用已经完成并产生费用，但网关收到完成标记之前连接中断，透明重试可能造成重复调用或重复计费。除非上游可靠实现幂等键，中间网关无法彻底消除该风险。

## 许可证

[MIT](LICENSE)
