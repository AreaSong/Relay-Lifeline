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

公开项目名称统一为 **Relay-Lifeline**，官方镜像使用 `ghcr.io/areasong/relay-lifeline`。为保证已有部署无损升级，二进制名、Go module、`RELAY_LIFELINE_*` 环境变量、`X-Relay-Lifeline-*` Header 和磁盘路径继续保留 `relay-lifeline` 兼容技术标识。

## 核心能力

- 重试全部上游错误，或只重试临时错误。
- 默认在 60～120 秒之间随机等待。
- 支持无限重试或限制最大尝试次数。
- 可遵循上游 `Retry-After`。
- 校验 Responses API 与 Chat Completions SSE 完成标记。
- 缓存完整响应期间发送 SSE 保活注释。
- 完整响应缓存，避免半截输出和重复交付工具调用。
- 单响应正文、进程总缓存和最小剩余磁盘三重资源保护。
- 客户端取消传播、并发限制、等待队列和恢复削峰。
- 请求时间线、有界内存历史、诊断和风险提醒。
- Signal Continuity（信号连续性）展示 Codex 到中转站的链路、活动请求、等待请求和下次重试；WebGL 不可用时自动降级为静态拓扑。
- 提供 `15m`、`1h`、`6h`、`24h` 窗口的内存可靠性、压力、错误分类和恢复指标。
- 对结构化上游错误进行白名单提取、脱敏和长度限制。
- 可筛选、暂停刷新和下载的实时结构化运行日志，不包含请求或响应正文。
- 展示当前网关 PID、Go 协程、调度器和内存快照；容器 PID 不会伪装成宿主机 PID。
- 可通过显式客户端 Header 关联 Codex 任务/会话 ID，并确保这些本地标识不转发到上游。
- 主动开启的临时诊断捕获：加密保存请求、每次 CPA 响应和最终响应，支持过滤预览、过滤下载与完整原文下载。
- 独立 Webhook 队列、事件过滤、投递重试、健康统计、有限投递历史和测试投递。
- 持续任务支持最大执行/失败次数、连续失败熔断和每轮安全审计摘要。
- 流量策略支持带 Journal 审计的 draft、simulate/replay、shadow、canary、full 和 rollback 发布流程。
- Shadow 流量具备幂等、采样、并发、每小时请求数和费用预算隔离。
- 自适应路由具备 SLO/错误预算保护、切换冷却、回退目标和自动停止信号。
- 多维治理支持预留、已知/未知用量结算，以及 enforce 模式下的故障闭合。
- 未知交付结果保留证据，并要求 Operator 明确确认成功、放弃或补偿重试。
- 历史与事故查询支持服务端筛选、稳定游标分页和关联请求钻取。
- 管理实时流使用版本化增量事件，支持断线游标补偿和保留缺口重置。
- UI、管理 API、CLI、日志、诊断和 Webhook 全部支持中英文。
- UI、日志和通知语言独立配置并可热更新。
- 独立管理密钥和默认仅本机监听。

Relay-Lifeline 始终只连接一个中转站。账号池、供应商选择、模型映射、权重和多个中转站之间的故障切换仍由 CPA 或指定上游负责。

## 快速启动

```bash
cp config.docker.example.yaml config.docker.yaml
cp .env.example .env
```

在 `.env` 中设置彼此不同且至少 24 字符的 `RELAY_LIFELINE_ADMIN_KEY`（Operator）和 `RELAY_LIFELINE_SENSITIVE_KEY`（Sensitive Data）。可选的 `RELAY_LIFELINE_VIEWER_KEY` 提供只读访问。再生成独立的 32 字节捕获密钥：

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
  response-body-idle-timeout: "90s"
```

## 重试语义

`all-errors` 会重试所有可恢复的不成功上游结果，包括 HTTP `4xx`、`5xx`、连接和超时错误、无效或空 JSON、`response.failed`、`response.incomplete` 以及截断的 SSE 流。本地缓存保护和不受支持的响应媒体类型不会重试。

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

数据面只交付非流式 JSON 和与请求匹配的 SSE。Responses 非流式响应必须包含 `status: completed`，Chat Completions 非流式响应必须包含 `choices` 数组；音频、图片、文件等二进制响应不会以错误的 JSON 类型透传。客户端压缩偏好不会直接转发，由 Go HTTP Transport 协商并自动解压 gzip 后再校验。

```yaml
stream:
  memory-limit: "64MiB"
  max-response-body: "512MiB"
  max-total-cache: "2GiB"
  temp-dir: ""
```

`memory-limit` 是单响应转存磁盘的阈值，不是硬上限。`max-response-body` 限制解压后的单响应正文，`max-total-cache` 限制当前进程所有活动响应缓存之和；落盘写入还会持续保留 `risk.minimum-free-disk` 指定的磁盘余量。

客户端传入的 `Idempotency-Key` 会在每次尝试中保持不变；Relay-Lifeline 不擅自生成该键，避免上游把第一次错误缓存到同一个键。断联检测同时使用下游请求 Context、连接关闭通知和心跳写入/Flush 错误，客户端离开后会取消活动中的上游调用。

## 管理控制台

控制台使用分层管理密钥：Viewer 只能读取脱敏状态和内容，Operator 可以执行运维操作，Sensitive Data 在 Operator 权限之上允许下载完整原文。现有 `RELAY_LIFELINE_ADMIN_KEY` 对应 Operator。

控制台可以：

- 查看活动请求、尝试次数、下次重试和安全失败详情。
- 查看请求时间线和有界持久化历史。
- 查看当前负载、分时可靠性与压力图表、七类稳定错误、恢复直方图和基于游标的运行事件。
- 查看长时间运行、尝试过多、鉴权错误、队列和磁盘风险。
- 执行不调用模型的一键诊断，并导出包含恢复检查、Journal 和备份完整性证据的脱敏 ZIP 包。
- 暂停或恢复全部请求、立即重试或取消指定请求。
- 创建具有执行上限、失败上限和连续失败熔断的持续任务，并查看最近 100 次执行审计。
- 修改重试、流、队列、历史、风险、通知、日志和语言设置。
- 保存前无落盘校验配置，明确显示热更新与需重启区段，再原子保存或从磁盘重新加载。
- 查看运行版本、revision、构建时间、镜像引用、运行时长、管理 API 版本和配置 schema 版本。
- 查看当前 Relay-Lifeline 进程的 PID、Go 协程、CPU 调度和内存资源快照。
- 查看实时结构化日志；按级别、事件或请求 ID 筛选并下载。
- 临时捕获接下来的指定数量请求，查看过滤正文，或下载过滤/完整原文 ZIP。

请求与事故时间线保存在经过校验的持久日志中，重启后会恢复；中断请求恢复为 `orphaned`，不会脱离原客户端连接自动重放。流量指标、运行事件和实时运行日志仍只属于当前进程，重启后清空；Journal 文件大小、启动回放、压实与健康指标反映持久存储的实时状态。监控固定保留 1,440 个 UTC 分钟桶，不受 `history.retention` 控制；进程尚未覆盖完整查询窗口时，`dataSince` 和 `complete` 会明确标识数据不完整。监听地址、管理开关、上游传输参数、服务超时和日志级别需要重启；重试、队列、历史、风险、语言和通知设置会在运行期间读取最新配置。

Signal Continuity 只展示网关实际观测到的状态，不会额外发送模型探针。Three.js 在本地按需加载；系统启用“减少动态效果”或页面进入后台时会暂停动画，WebGL 初始化失败或 Context 丢失时切换到静态拓扑，状态数据和控制功能仍然可用。

## 流量策略、治理与未知交付

流量策略必须通过可审计的控制面流程发布。旧的 `PUT /admin/api/policies`，以及会修改 `traffic-policy` 的普通 `PUT /admin/api/config`，都会返回 `POLICY_RELEASE_REQUIRED`，不会直接热应用未经审核的路由或拒绝规则。Operator 应按以下顺序操作：

```text
draft -> simulate/replay -> shadow -> canary -> full
                                      \-> rollback（保留的发布 revision）
```

需要鉴权的接口包括 `PUT /admin/api/policies/draft`、`GET /admin/api/policies/releases`、`POST /admin/api/policies/simulate`、`POST /admin/api/policies/replay`、`POST /admin/api/policies/publish` 和 `POST /admin/api/policies/rollback`。发布和回滚必须携带当前 `configRevision`；草稿或目标 revision 过期时会冲突失败，不会覆盖其他 Operator 的修改。每个 prepare、publish、abort、rollback 转换都会写入 `policy-releases.jsonl`，并在重启后与活动配置 revision 对账。`GET /admin/api/policies/status` 和 `/decisions` 提供运行计数及有界决策证据。

`mode: observe` 只记录建议，永不改变生产路由。在 `mode: enforce` 下，`draft` 和 `shadow` 仍不会强制客户端流量；`canary` 使用由 Request ID 派生的稳定 SHA-256 分桶，只对配置比例内的请求强制；`full` 对所有命中的请求强制。决策证据区分 `recommendedTargetId` 与实际强制的 `targetId`，因此 dry-run、未命中的 canary 或 observe 决策不能被误认为真实改路由。使用 `POST /policies/simulate?source=draft` 可测试已保存草稿，不会改变自适应状态，也不会调用上游；`/policies/replay` 可使用捕获 ID 或脱敏请求元数据重复生成证据。

Shadow 流量只在主请求成功后异步发送，与生产熔断器和自适应评分隔离，并带有 `X-Relay-Lifeline-Shadow: 1`。如果目标就是主目标，或任一保护条件不满足，Shadow 会跳过。一次 Shadow 必须同时通过 Request ID 稳定采样、`require-idempotency`、SLO 健康、正文大小、最大并发、每小时请求数和每小时费用预留检查。`/policies/status` 分开统计 planned、sent、skipped、failed、预留费用和实际费用；Shadow 失败不会改变主请求结果。

自适应路由只给已关闭、观测数足够且延迟合格的目标评分。SLO/错误预算下限、burn rate 保护和失败率保护都可以自动停止自适应；切换冷却时间防止目标抖动，配置的 fallback 目标会在保护停止或没有合格目标时使用。发布新的策略 revision 会确认并解除自动停止；解除前必须先核对新的信号与 SLO。

治理会在选择目标前预留有界的 Token 和费用容量，然后绑定实际选中的上游，并为每次尝试结算。预算可以按全局或 `principal`、`tenant`、`model`、`upstream` 作用域配置；存在 tenant 预算时必须提供租户 Header。`reservation-min/max-*` 限制预估范围，`soft-threshold-percent` 和 `forecast-window` 只产生告警信号，在 observe 模式下不会拒绝请求。`GET /admin/api/governance/status`、`/health/summary`、`/slo` 和 Prometheus 治理指标会显示预留、已知/未知结算、拒绝原因和 ledger 健康状态。

只有在持久化 usage ledger 和恢复路径经过演练后，才应设置 `governance.mode: enforce`。Ledger 写入失败时，admission、重试尝试预留和结算都会故障闭合；enforce 开启时 `/readyz` 也会检查 usage ledger。observe 模式会标记 `persistenceDegraded` 后继续处理，因此仍必须处理告警。`unknown-usage-policy: observe` 会把缺少权威 usage 的响应记为未知；`unknown-usage-policy: deny` 会在同一预算窗口内拒绝后续共享该预算的 admission，直到未知用量滚出窗口，原响应不会被事后改写。

某次尝试已经写入上游但没有收到响应 Header 时，请求会进入 `uncertain`，而不是静默重试（默认 `lifecycle.allow-uncertain-retry` 为 false）。时间线只保存有界证据：尝试阶段、目标、状态/分类、是否写入、幂等键哈希、请求大小/延迟和上游 Request ID，不保存原始正文。Operator 必须先预览动作，再使用同一身份提交不超过 500 字符的原因：

```bash
curl -H "Authorization: Bearer $RELAY_LIFELINE_ADMIN_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"action":"confirm_success"}' \
  "http://127.0.0.1:8318/admin/api/requests/$REQUEST_ID/uncertain/preview"

curl -H "Authorization: Bearer $RELAY_LIFELINE_ADMIN_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"action":"confirm_success","confirmationToken":"TOKEN_FROM_PREVIEW","reason":"已在上游审计中核对 Provider Request ID。"}' \
  "http://127.0.0.1:8318/admin/api/requests/$REQUEST_ID/uncertain/resolve"
```

预览 Token 两分钟后过期，并绑定请求、动作和身份。支持的动作是 `confirm_success`（业务成功，不再重试）、`abandon`（终止并记为失败）和 `request_compensation`（恢复正常重试路径）。当上游可能已经扣费时，不要盲目重试。`/admin/api/health/summary`、`/admin/api/slo`、Webhook 和 `relay_lifeline_uncertain_*` Prometheus 指标会报告未决数量、最老年龄、处理目标、SLO 超时和处理结果。

请求、事故、持续任务、usage ledger 和策略发布 Journal 都是经过校验的哈希链。配置 `RELAY_LIFELINE_JOURNAL_HMAC_KEY` 后，还会校验外部 HMAC 锚点；启动时发现链损坏或截断会拒绝启动，`-journal-verify` 只读。每小时压实会原子重建保留链，并保留活动实体。重启时未完成请求会成为 `orphaned`，绝不自动重放；治理预留会对账，策略 prepare 只有在活动 revision 匹配时才 finalize，否则明确 abort。持久化目录必须位于可靠卷上；强制修复前先检查 `readyz`、`/admin/api/persistence/status` 和 Journal 指标。

## 监控 API

需要管理端 Bearer 鉴权的接口包括：

- `GET /admin/api/metrics?window=15m|1h|6h|24h`：返回汇总、分钟序列、当前负载和恢复直方图，默认窗口为 `1h`。
- `GET /admin/api/metrics/errors?window=15m|1h|6h|24h`：返回稳定错误分类分布，默认窗口为 `24h`。
- `GET /admin/api/events?after=<cursor>&limit=<1-200>`：读取有界运行事件环。响应包含 `nextAfter`、`oldestAfter`、`hasMore` 和 `hasGap`，客户端可据此续读或发现旧事件已被覆盖。
- `GET /admin/api/runtime-logs?tail=true&limit=<1-500>`：读取最新结构化日志；也可使用 `after` 游标续读。响应包含 `entries`、`nextAfter`、`oldestAfter`、`hasMore` 和 `hasGap`，筛选值和级别均有严格边界。
- `GET /admin/api/history?cursor=<cursor>&limit=<1-200>&from=<RFC3339>&to=<RFC3339>&state=<state>&q=<text>`：服务端筛选和稳定游标分页的请求历史。
- `GET /admin/api/incidents` 使用相同分页参数；`GET /admin/api/incidents/{id}` 返回事故及最多 100 条仍在保留期内的关联请求。
- `GET /admin/api/notifications/status` 与 `/notifications/deliveries` 返回队列、成功/失败/丢弃统计和最近投递；状态只返回 HMAC 签名状态和 Key ID，不返回 Secret；Operator 可调用 `POST /notifications/test`。
- `GET /admin/api/stream?after=<cursor>` 首次发送 `sync`，随后只发送 `update`；事件含 `version`、`sequence`、`type` 和 `data`，游标过旧时发送 `reset`。

Prometheus 端点提供 `relay_lifeline_journal_*` 指标，包括条目数、字节数、启动回放、最近压实、写入健康和压实健康状态；`relay_lifeline_process_*` 提供 PID、Go 协程、堆内存、系统内存和 GC 次数。

客户端可选传入 `X-Relay-Lifeline-Client-ID` 与 `X-Relay-Lifeline-Task-ID`。为兼容 Codex 包装器，也接受 `X-Codex-Session-ID` 与 `X-Codex-Thread-ID`。值必须不超过 128 字节且只能包含安全标识字符；它们会作为“客户端声明、未经验证”的关联元数据进入活动请求、历史和运行日志，但不会转发到上游。不要把密钥或 Token 放入这些 Header。响应会返回 `X-Relay-Lifeline-Request-ID`，用于与网关自己的时间线关联。Codex app-server 的 `threadId` 是会话逻辑 ID，不是操作系统 PID；后台终端返回的 `processId` 是 app-server 层的进程标识，另有可能为空的 `osPid` 字段，二者都不能直接当作宿主机进程号。Codex 未显式发送这些 Header 时，网关无法从 HTTP 流量推导任务 ID，也无法从 Docker 容器内可靠获得宿主机 Codex PID。

诊断 ZIP 分别包含脱敏配置、诊断、时间线、运行日志、指标、事故、恢复检查、Journal 摘要和配置备份元数据。它不包含请求/响应正文、安全错误详情、备份正文或密钥。单请求时间线最多保留 100 条事件；超限时保留首事件和最近事件，并通过 `eventsTruncated`、`droppedEvents` 明确报告省略数量。

这些接口与其他管理 API 使用相同的管理密钥和本地化规则。

每个管理响应都包含兼容 Header `X-Relay-Lifeline-API-Version`。`GET /admin/api/meta` 返回当前构建身份。配置文件使用 `schema-version: 5`；schema 1 至 4 配置会在内存中迁移且不会覆盖源文件，未知的未来版本会被拒绝。`POST /admin/api/config/validate` 返回准确的变更计划，不修改内存或磁盘配置。

## 双语配置

Web UI 可实时切换语言，并将选择保存到 `localStorage`。管理 API 请求发送 `Accept-Language`，响应返回 `Content-Language`。

登录页和控制台均支持 `system`、`light`、`dark` 三态主题。`system` 跟随操作系统；显式选择浅色或深色后会保存到 `localStorage`。

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
- 监控指标不包含 Prompt、响应正文、Authorization 或原始上游错误。错误只使用稳定的 `transport`、`protocol`、`auth`、`rate_limit`、`client`、`server`、`http` 分类；运行事件只包含稳定代码和有界元数据。
- 临时响应文件权限为 `0600`，交付或失败后删除。
- 诊断导出会移除 URL 凭据、Query、Webhook 地址和错误详情。
- 请求与事故时间线使用 SHA-256 哈希链日志；启动时拒绝损坏、截断或被修改的日志，过期实体通过 `0600` 权限的原子压实清理并重建保留链。
- 管理控制台使用严格安全 Header，不依赖第三方 CDN。
- 临时捕获默认不活动；正文使用分块 AES-256-GCM 加密，认证 Header 永不落盘。
- 完整原文不能在线预览，只能在明确确认后流式解密到下载 ZIP，磁盘不生成明文 ZIP。
- 捕获默认保留 72 小时；空间或最低磁盘阈值不足时停止保存正文，不阻塞代理请求。
- 成功响应和达到终止条件的失败响应都会标记为最终响应；服务重启时尚未完成的捕获会落为“服务重启中断”，不会永久停留在活动状态。
- 捕获支持活动 Key ID 和历史 Key Ring。`RELAY_LIFELINE_CAPTURE_KEY` 是兼容旧部署的 `legacy` 密钥；使用 `RELAY_LIFELINE_CAPTURE_ACTIVE_KEY_ID` 与 JSON 对象 `RELAY_LIFELINE_CAPTURE_KEYRING` 配置新密钥。先同时保留新旧密钥并重启，在捕获页执行“重包裹到活动密钥”，确认旧 Key ID 记录数为零且未解析记录为零后，才能移除旧密钥。

不要把管理端直接暴露到公网。需要远程访问时，应增加 TLS、访问控制和可信网络边界。

## 诊断与通知

诊断会检查配置、文件访问、管理密钥长度、CPA DNS/TCP 连通性、缓存权限和磁盘容量。上游检查只建立 TCP 连接，不发送模型请求，也不消耗 Token。

Webhook 可通知持续故障、恢复、长时间运行、尝试过多、鉴权错误、队列压力和磁盘压力。Payload 同时包含稳定的 `eventCode`、数值型 `elapsedSeconds` 和本地化文字。通知使用有界独立队列，不阻塞模型请求链路；管理面只保留最近 100 条投递结果，不保存 Payload 或 Webhook 地址。每个已配置的 Webhook 都必须同时设置 `RELAY_LIFELINE_WEBHOOK_SIGNING_KEY_ID` 与 `RELAY_LIFELINE_WEBHOOK_SIGNING_SECRET`，每次投递都会带上 `X-Relay-Lifeline-Signature-Key-ID`、`X-Relay-Lifeline-Signature-Timestamp` 和 `X-Relay-Lifeline-Signature`；接收方应按 `v1=<hex HMAC-SHA256(timestamp + "." + 原始 Payload)>` 校验后再接受。Secret 只通过进程环境变量提供；Webhook 已配置但签名配置不完整或 Secret 少于 32 字节时，服务拒绝启动。

持续任务支持严格的 `maxTokens` 上限。累计值只来自上游权威 `usage.total_tokens`；缺少 usage 时任务以 `usage_missing` 暂停，不把输入/输出 Token 相加，也不估算费用。达到上限后，当前执行结束即任务到期；Relay-Lifeline 不实现费用估算。

## 开发

要求 Go 1.25+、Node.js 22+，集成验证还需要 Docker。

```bash
make check
make race
./scripts/ci-integration.sh
make docker-build
./scripts/container-smoke.sh relay-lifeline:dev
```

部分 macOS/Xcode 与旧 Go 工具链组合运行测试二进制时需要外部链接。如果内部链接器报告缺少 `LC_UUID`，执行 `go test -ldflags=-linkmode=external ./...`；发布与 CI 固定使用 Go 1.25 系列。

更多信息见[运维手册](docs/operations.zh-CN.md)、[架构说明](docs/architecture.zh-CN.md)、[贡献指南](CONTRIBUTING.zh-CN.md)和[安全策略](SECURITY.zh-CN.md)。

## 回滚

Relay-Lifeline 不修改上游账号或 API Key。将客户端 `base_url` 改回 CPA 或原中转站即可立即绕过网关。每次持久化配置前，当前文件都会以 `0600` 权限复制到 `server.config-backup-dir`；未配置时使用配置文件旁的 `.relay-lifeline-backups`，只保留最新 10 份。升级已部署实例时还应保留旧镜像。

Docker 构建接受 `VERSION`、`REVISION`、`BUILD_TIME` 参数；运行镜像引用通过 `RELAY_LIFELINE_IMAGE_REF` 传入，使设置页和 `/admin/api/meta` 能准确标识待回滚的部署版本。

## 已知风险

如果上游调用已经完成并产生费用，但网关收到完成标记之前连接中断，透明重试可能造成重复调用或重复计费。除非上游可靠实现幂等键，中间网关无法彻底消除该风险。

## 许可证

[Apache-2.0](LICENSE)
