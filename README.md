# Relay Lifeline

**中转生命线**是面向 OpenAI-compatible AI API 中转站的请求保活、延迟重试与故障恢复网关。

它位于 Codex、IDE 或其他 AI 客户端与现有中转站之间。当中转站暂时没有可用通道、触发限流、连接中断或返回协议错误时，Relay Lifeline 保持客户端连接，等待后重新提交同一请求，直到成功、达到策略上限或客户端取消。

> 社区项目，与 CLIProxyAPI 及任何上游模型提供商均无官方关联。

## 工作方式

```text
AI 客户端
    │  原 API Key，仅修改 base_url
    ▼
Relay Lifeline :8318
    │  Authorization 原样透传
    ▼
任意 OpenAI-compatible 中转站
    ▼
上游模型提供商
```

核心能力：

- 所有错误或仅临时错误两种策略
- `60–120` 秒随机延迟、无限或有限次数重试
- 遵循上游 `Retry-After`
- Responses API 和 Chat Completions SSE 完整性校验
- 等待期间 SSE 心跳，避免客户端空闲超时
- 完整响应缓存，防止半截内容和重复工具调用
- 客户端取消传播、动态并发限制、等待队列和恢复削峰
- 脱敏状态、暂停/恢复、立即重试、取消和配置热更新
- 独立管理密钥、回环地址绑定和敏感日志保护

## 快速启动

```bash
cp config.docker.example.yaml config.docker.yaml
cp .env.example .env
```

编辑 `.env`，设置一个长随机管理密钥；再按实际中转站地址修改 `config.docker.yaml` 的 `upstream.base-url`。
Linux 用户应将 `RELAY_LIFELINE_UID` 和 `RELAY_LIFELINE_GID` 设置为 `id -u`、`id -g` 的结果，确保管理页可以保存绑定挂载的配置文件。

```bash
docker compose up -d --build
curl http://127.0.0.1:8318/healthz
```

管理控制台：<http://127.0.0.1:8318/admin/>

客户端只需将原来的中转地址改为 Relay Lifeline，API Key 不变：

```toml
[model_providers.relay]
base_url = "http://127.0.0.1:8318/v1"
wire_api = "responses"
```

## CLIProxyAPI 示例

CLIProxyAPI 已在主机 `8317` 端口运行时，Docker 配置使用：

```yaml
upstream:
  base-url: "http://host.docker.internal:8317"
```

如果两个服务位于同一 Docker 网络，使用 CLIProxyAPI 的服务名：

```yaml
upstream:
  base-url: "http://cli-proxy-api:8317"
```

## 重试语义

`all-errors` 会重试所有上游非成功结果，包括 `400`、`401`、`403`、`429`、`5xx`、连接错误、无法解析的响应、`response.failed`、`response.incomplete` 和截断流。

```yaml
retry:
  enabled: true
  mode: "all-errors"
  min-interval: "60s"
  max-interval: "120s"
  max-attempts: 0
  honor-retry-after: true
```

`max-attempts: 0` 表示无限重试。项目示例按“所有错误、无限重试”的目标配置；永久错误无限重试可能长期占用连接，可按实际需要切换为 `transient-errors` 或设置次数上限。

合法、完整的 2xx 响应才会停止重试。助手在正常响应中拒绝回答不属于 API 错误。客户端断开或主动取消会立即终止当前上游请求和等待循环。

## 管理控制台

控制台使用 `RELAY_LIFELINE_ADMIN_KEY` 登录，可执行：

- 查看活动请求、失败原因、尝试次数和下次重试时间
- 暂停或恢复全部重试
- 立即重试或取消指定请求
- 修改错误范围、间隔、次数、心跳、缓存、队列和通知
- 原子保存或重新加载 YAML 配置

监听地址、管理开关、上游连接参数和日志级别变更需要重启；重试、流式、队列及通知策略会应用于后续阶段或新请求。

## 安全

- Docker 示例只发布到主机 `127.0.0.1:8318`。
- 客户端 Authorization 只在内存中透传。
- 默认不记录请求体、响应体或 Authorization。
- 管理密钥与客户端 API Key 完全分离。
- 响应转存文件使用 `0600` 权限，并在交付或失败后删除。
- 管理页面设置严格 CSP，不依赖第三方 CDN。

不要将管理页面直接暴露到公网。需要远程访问时，应在前面增加 TLS、访问控制和受信任网络边界。

## 通知

可配置一个 HTTP(S) Webhook。请求持续失败超过 `stalled-after` 后发送一次 `stalled` 事件；恢复后发送一次 `recovered` 事件。通知内容只包含请求 ID、尝试次数、耗时和脱敏原因。

## 开发

```bash
make check
make docker-build
```

本机 Go 1.22 在部分新版 macOS 上需要 `CGO_ENABLED=0`，Makefile 和 CI 已统一使用纯 Go 构建。

更多实现细节见 [架构说明](docs/architecture.md)，安全问题见 [安全策略](SECURITY.md)。

## 回滚

Relay Lifeline 不修改中转站账号或 API Key。将客户端 `base_url` 改回原中转站地址即可立即绕过网关。

## 风险

如果上游已经完成并产生费用，但连接在结果返回前中断，透明重试可能产生重复调用或重复计费。除非上游提供并正确实现幂等键，否则无法从中间网关彻底消除此风险。

## License

[MIT](LICENSE)
