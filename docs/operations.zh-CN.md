# Relay-Lifeline 运维手册

[English](operations.md)

## 部署前检查

- Docker Engine 和 Docker Compose 可用。
- CPA 或其他 OpenAI-compatible 中转站已有可访问地址。
- 准备三个互不相同且不少于 24 字符的管理密钥；Viewer 可不配置。
- 准备独立的 32 字节 Base64 捕获密钥。
- 管理端默认只绑定 `127.0.0.1:8318`，不要直接暴露到公网。

生成本地密钥：

```bash
openssl rand -base64 36
openssl rand -base64 32
```

不要把生成结果写入仓库、Issue、日志或截图。

## 安装

```bash
cp config.docker.example.yaml config.docker.yaml
cp .env.example .env
chmod 600 .env config.docker.yaml
docker compose up -d
curl -fsS http://127.0.0.1:8318/healthz
curl -fsS http://127.0.0.1:8318/readyz
```

启用持久化后，如果请求或事故 Journal（追加日志）已关闭、不可写或最近写入失败，`/readyz` 会返回 `503 unavailable`。Prometheus 的 `relay_lifeline_journal_*` 指标会暴露条目数、文件大小、启动回放、最近压实以及日志/压实健康状态。

## 迁移、恢复检查与故障演练

以下命令应针对已停止的实例或文件副本执行。`-recovery-check` 只读；`-config-migrate` 会先生成权限为 `0600` 的备份，再原子写入 schema 5。schema 1 至 4 均可迁移；schema 5 新增 OIDC 管理认证，并为迁移配置保留本地应急访问。

```bash
relay-lifeline -config /etc/relay-lifeline/config.yaml -config-validate
relay-lifeline -config /etc/relay-lifeline/config.yaml -config-migrate
relay-lifeline -config /etc/relay-lifeline/config.yaml -recovery-check
relay-lifeline -journal-verify /var/lib/relay-lifeline/events/requests.jsonl
```

隔离演练重试链时，在测试端口启动 `fault-upstream`，并让一份临时 Relay-Lifeline 配置指向它。不要为了演练修改生产 CPA 上游地址。

```bash
go run ./cmd/fault-upstream -listen 127.0.0.1:18317 \
  -sequence 401,429,503,invalid-json,truncated-sse,success
./scripts/ci-integration.sh
```

诊断 ZIP 新增 `recovery-check.json`、`journal-summary.json` 和 `config-backups.json`。备份记录只包含文件名、修改时间、大小、SHA-256、源 schema 和校验状态；诊断包绝不包含原始请求体、响应体或备份正文。

CPA 与 Relay-Lifeline 位于同一个 Compose 网络时，上游使用服务名：

```yaml
upstream:
  base-url: "http://cli-proxy-api:8317"
```

CPA 位于宿主机时使用：

```yaml
upstream:
  base-url: "http://host.docker.internal:8317"
```

客户端只修改 Base URL，原业务 API Key 不变：

```toml
[model_providers.relay_lifeline]
base_url = "http://127.0.0.1:8318/v1"
wire_api = "responses"
```

## 关键配置

推荐生产基线：

```yaml
server:
  shutdown-timeout: "3m"

upstream:
  connect-timeout: "10s"
  response-header-timeout: "30s"
  response-body-idle-timeout: "90s"

retry:
  enabled: true
  mode: "all-errors"
  min-interval: "60s"
  max-interval: "120s"
  max-attempts: 0
  honor-retry-after: true

stream:
  heartbeat-interval: "15s"
  memory-limit: "64MiB"
  max-response-body: "512MiB"
  max-total-cache: "2GiB"

queue:
  max-active: 8
  max-waiting: 100
  recovery-spacing: "2s"

persistence:
  enabled: true
  directory: "/var/lib/relay-lifeline/events"
  retention: "336h"
  sync-writes: true

lifecycle:
  track-uncertain-delivery: true
  preserve-idempotency-key: true
  generate-idempotency-key: false
  max-request-duration: "0s"
  client-disconnect-policy: "cancel"
```

`response-body-idle-timeout` 从最后一段上游正文数据开始计时。超过阈值会关闭该次响应并进入重试，防止已收到响应头但正文永久停顿。

`memory-limit` 是转存临时文件的阈值。单个解压后响应超过 `max-response-body`、进程活动缓存超过 `max-total-cache`，或临时目录无法保留 `risk.minimum-free-disk` 时，该请求会立即失败且不会重试。容量字段可在设置页热更新；降低总预算不会删除已有缓存，新尝试会按新预算限制，已开始的尝试使用其启动时配置快照。

生产变更前应分别演练：略高于单响应上限的正文只尝试一次；并发缓存触及总预算后拒绝扩张；gzip JSON 能解压并完成；音频或文件媒体类型被明确拒绝而不是伪装成 JSON。

## 角色权限

| 角色 | 密钥 | 权限 |
|---|---|---|
| Viewer | `RELAY_LIFELINE_VIEWER_KEY` | 脱敏只读状态、日志和过滤捕获 |
| Operator | `RELAY_LIFELINE_ADMIN_KEY` | Viewer 权限，加配置、暂停、重试、取消和捕获运维 |
| Sensitive Data | `RELAY_LIFELINE_SENSITIVE_KEY` | Operator 权限，加完整原文下载 |

完整原文下载还必须发送 `X-Relay-Lifeline-Confirm: download-sensitive`。角色密钥和 CPA 业务 API Key 必须分开管理。

## 捕获与密钥轮换

捕获默认关闭，开启后成功与失败响应都会加密保存，默认保留 72 小时。过滤预览和过滤下载会脱敏；完整原文只允许 Sensitive Data 角色下载。

轮换顺序：

1. 生成新 32 字节 Base64 密钥和新 Key ID。
2. 将旧、新密钥同时写入 `RELAY_LIFELINE_CAPTURE_KEYRING`，把新 ID 设置为 `RELAY_LIFELINE_CAPTURE_ACTIVE_KEY_ID`。
3. 重建容器，确认捕获页同时显示两个 Key ID。
4. 执行“重包裹到活动密钥”。
5. 确认旧 Key ID 记录数为 0，`unresolved` 为 0。
6. 从 Key Ring 移除旧密钥并再次重建容器。
7. 抽查过滤预览和完整原文下载。

轮换只重新加密小型数据密钥，不重新加密大正文。旧密钥仍被记录引用时不得删除。

## 故障处理

请求状态变化：

```text
queued -> requesting -> waiting -> requesting -> completed
```

- `waiting`：上游失败，正在等待下一次重试。
- `requesting`：当前正在请求上游。
- `queued`：活动并发已满，尚未取得上游名额。
- `interrupted`：服务重启时未完成的捕获记录，正文证据仍可检查。
- `uncertain`：请求可能已写入上游，但响应头尚未返回；再次尝试存在重复调用风险。
- `orphaned`：进程重启前未完成的旧请求，仅保留历史，不会脱离原客户端自动重放。

处理顺序：

1. 查看管理后台“请求”和“日志”页的 Request ID。
2. 运行诊断，确认 DNS、TCP、配置、磁盘和捕获密钥状态。
3. 检查 CPA 自身状态；Relay-Lifeline 不管理账号池、额度或模型路由。
4. 必要时使用“立即重试”，不要重复提交多个相同客户端请求。
5. 导出诊断包；诊断包不含请求体、响应体和完整原文。

常用命令：

```bash
docker compose ps
docker compose logs --tail=200 relay-lifeline
curl -fsS http://127.0.0.1:8318/healthz
curl -fsS http://127.0.0.1:8318/readyz
relay-lifeline -config /etc/relay-lifeline/config.yaml -config-validate
relay-lifeline -config /etc/relay-lifeline/config.yaml -doctor
relay-lifeline -journal-verify /var/lib/relay-lifeline/events/requests.jsonl
```

## 流量策略发布 Runbook

以下写操作都使用 Operator 凭据。开始前记录当前目标 `configRevision`；出现冲突表示已有其他变更落地，应重新读取草稿，不要强制覆盖。

1. 检查 `GET /admin/api/config/state`、`GET /admin/api/policies`、`GET /admin/api/policies/releases`、`GET /admin/api/policies/status`、`GET /admin/api/slo` 和 `GET /admin/api/health/summary`，确认目标 `configRevision`、目标 ID、当前阶段、SLO 与 Journal 健康。
2. 使用 `PUT /admin/api/policies/draft` 保存候选；编辑已有草稿时带上之前的 `draftRevision`。用 `POST /admin/api/policies/simulate?source=draft` 测试代表性的 method/path/model/principal，再用 `POST /admin/api/policies/replay` 重放脱敏捕获或请求样本。必须确认返回 `dryRun: true`、`enforced: false`，且没有产生上游请求。
3. 候选有 Shadow 目标时先发布 `stage: shadow`。观察 `/policies/status` 的 `shadowPlanned`、`shadowSent`、`shadowSkipped`、`shadowFailed`、`shadowReservedCostMicros` 和 `shadowActualCostMicros`；同时确认 Shadow 结果没有改变主目标熔断器或自适应计数。失败、跳过或费用超出变更窗口时停止并回滚。
4. 发布有界的 `stage: canary`，明确设置 `canaryPercent`（1-100）。检查最近决策的 Request ID 稳定分桶：`canarySelected` 应符合预期比例；只有 enforce 模式且被选中的决策才能出现 `enforced: true` 和非空 `targetId`。只有 `recommendedTargetId` 而没有 `targetId` 的决策没有实际改路由。
5. 只有在 canary 窗口满足可用性、恢复延迟、错误预算、上游熔断和治理预算后，才提升到 `stage: full`。保留上一版本 release revision 以便回滚。

紧急回滚时，如果影响面仍在扩大，先暂停或排空新请求，再用 release history 中的已知正常 `policyRevision` 和当前 `configRevision` 调用 `POST /admin/api/policies/rollback`。恢复后检查 `/policies/releases`、`/policies/status`、`/config/state`、`/health/summary`，并执行一次不消耗模型 Token 的连通性检查，再恢复流量。回滚本身也会写入 Journal，不要手工编辑 `policy-releases.jsonl`。

自适应路由需要单独观察：将 `adaptiveStopped`、`adaptiveStopReason`、`adaptiveSwitches`、`adaptiveLastTargetId`、`adaptiveLastScore` 与 `/slo` 的 `errorBudgetRemaining`、`burnRate` 一起查看。`slo_guard`、`burn_rate_guard`、`failure_rate_guard` 和 `adaptive_auto_stopped` 是停止信号，不是普通目标瞬时错误；切换冷却期间保持上一个合格目标是预期行为。修复信号后发布新的策略 revision 才会确认自动停止；提升前必须核对 fallback 目标和 SLO。

## Governance Ledger Runbook

`GET /admin/api/governance/status` 返回预留、各作用域用量、已预留容量、未知用量、拒绝原因和 ledger 状态。用 `GET /admin/api/persistence/status`、`/health/summary`、`/readyz` 以及 Prometheus 的 `relay_lifeline_governance_*`、`relay_lifeline_journal_*` 指标交叉核对。

- `governance.mode: enforce` 下，如果 usage ledger 为 `degraded` 或 `/readyz` 返回 `503`，应停止或排空新流量。配置的 ledger 无法写入时，admission、重试尝试预留和结算都会故障闭合。保留持久卷，检查所有者/权限、磁盘空间和失败阶段；停止实例后在副本上通过 `-journal-verify`，再重启。
- `observe` 模式会以 `persistenceDegraded` 暴露 ledger 写失败，但请求链路可能继续。若预算承担安全职责，应把这视为阻断发布的事故；没有经过演练的 ledger 不应直接切换到 enforce。
- `unknownUsagePolicy: deny` 下，先处理预算窗口中 `unknownUsage` 非零的条目。请求已写入但缺少权威 usage 时会记录未知结算，不会估算 Token 或费用；应修复上游 usage，或等待窗口滚动，再确认计数和预留已结清。
- 不要通过删除或截断 `usage-ledger.jsonl` 清空预算。启动会重放预留/结算并对账中断预留，压实过程会用 heartbeat 保护活动预留。

## Uncertain 交付处理 Runbook

`uncertain` 表示网关可能已经向上游写入正文，但没有收到响应 Header；默认不会自动重试。先从 `/admin/api/status` 找到 Request ID，再查看 `/admin/api/requests/{id}/timeline`，将证据与上游审计记录比对后再决定动作。

```bash
curl -H "Authorization: Bearer $RELAY_LIFELINE_ADMIN_KEY" \
  "http://127.0.0.1:8318/admin/api/requests/$REQUEST_ID/timeline"
curl -H "Authorization: Bearer $RELAY_LIFELINE_ADMIN_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"action":"confirm_success"}' \
  "http://127.0.0.1:8318/admin/api/requests/$REQUEST_ID/uncertain/preview"
```

预览返回证据和两分钟有效、绑定身份的确认 Token。向 `/uncertain/resolve` 提交 Token 时必须带非空原因（最多 500 个 Unicode 字符）。三选一：Provider 审计确认完成时使用 `confirm_success`；业务上应视为失败时使用 `abandon`；明确批准重试且已理解幂等/扣费风险时使用 `request_compensation`。过期 Token、不同动作、不同身份或已经处理的请求都应按冲突处理；不要把 Token 放入脚本或日志。

持续观察 `/admin/api/health/summary`、`/admin/api/slo`、`uncertain_slo_breach` Webhook，以及 `relay_lifeline_uncertain_open`、`relay_lifeline_uncertain_oldest_seconds`、`relay_lifeline_uncertain_slo_healthy`。应在配置的处理目标前处理最老记录；超过目标后 health component 会降级。`orphaned` 不同于 `uncertain`：它是重启时未完成的旧请求，只保留历史，永不重放。

## Journal 校验与恢复

持久化目录包含 `requests.jsonl`、`incidents.jsonl`、`repeat-tasks.jsonl`、`usage-ledger.jsonl` 和 `policy-releases.jsonl`。它们都是哈希链；配置 `RELAY_LIFELINE_JOURNAL_HMAC_KEY` 后还会保护外部 `.anchor` 文件。应在停止实例或只读副本上校验：

```bash
for journal in requests incidents repeat-tasks usage-ledger policy-releases; do
  relay-lifeline -journal-verify \
    "/var/lib/relay-lifeline/events/${journal}.jsonl" || exit 1
done
relay-lifeline -config /etc/relay-lifeline/config.yaml -recovery-check
```

启动会拒绝格式错误、序号缺口、哈希不匹配或锚点不匹配。保留原始卷用于取证；应恢复已知正常的卷/配置备份，不要删除某一行或手工重建链。每小时压实是原子的，会重新计算保留条目的哈希；超出保留期的请求/事故会清理，活动实体会保留。策略 prepare 只有在活动策略 revision 匹配时才 finalize，否则写为 abort。恢复后，在接收流量前检查 `/readyz`、`/admin/api/persistence/status`、`/admin/api/governance/status`、`/admin/api/policies/releases` 和 Prometheus 压实健康指标。

## 重启与排空

Compose 的 `stop_grace_period` 应大于 `server.shutdown-timeout`。收到退出信号后，服务停止接收新连接、把等待中的请求唤醒为一次立即重试，并同步等待活动 Handler 排空。

强制终止进程无法保留旧 TCP 连接。OpenAI-compatible 协议没有让新进程接管旧连接的请求句柄；此时客户端必须重连。未完成捕获会安全标记为 `interrupted`，不会冒充成功。

## 升级

```bash
docker image tag ghcr.io/areasong/relay-lifeline:CURRENT relay-lifeline:rollback
cp config.docker.yaml config.docker.rollback.yaml
docker compose pull
docker compose up -d
curl -fsS http://127.0.0.1:8318/healthz
```

升级后检查：

- `/admin/api/meta` 的版本、revision 和构建时间。
- 诊断 8 项全部通过。
- 捕获 `unresolved` 为 0，旧记录仍可读取。
- Viewer、Operator、Sensitive Data 权限仍然隔离。
- 使用不消耗模型 Token 的健康检查或 CPA 根路径检查连通性。

## 回滚

1. 先把客户端 Base URL 改回 CPA，可立即绕过网关。
2. 停止新请求并等待活动请求收敛。
3. 同时恢复旧镜像和该版本对应的配置备份。
4. 重建容器并检查健康、版本、诊断和捕获可读性。

旧版本可能拒绝新字段，因此不能只回滚镜像而保留更新后的配置；必须恢复迁移前配置，不能让旧二进制读取 schema 5 文件。配置保存操作会创建最多 10 份 `0600` 回滚副本。

## 数据与保留

### 控制面游标与通知

- 历史和事故列表默认每页 100 条，最多 200 条；自动化调用必须保存响应的 `nextCursor`，不要把游标解析为业务字段。
- 管理实时流优先使用浏览器自动发送的 `Last-Event-ID`，非浏览器客户端可传 `after`。收到 `reset` 表示旧游标已超出 512 条事件保留环，应以该事件的全量数据替换本地状态。
- Webhook 状态和最近投递可分别从 `/admin/api/notifications/status`、`/admin/api/notifications/deliveries` 查看。测试投递会真实调用已配置端点，只允许 Operator 执行；历史最多 100 条且不含 Payload 和目标 URL。
- 持续任务出现 `circuitOpen` 时不会自动恢复。检查最近执行审计和上游状态后，由 Operator 显式恢复；达到执行/失败上限的 `expired` 任务不能再次运行。

### Webhook 签名与严格 Token 预算

在 Webhook URL 同一运行环境中设置 `RELAY_LIFELINE_WEBHOOK_SIGNING_KEY_ID` 与 `RELAY_LIFELINE_WEBHOOK_SIGNING_SECRET`。Secret 至少需要 32 字节；只配置其中一项时服务会在启动阶段拒绝。发送方对精确的 UTF-8 原始请求体按 `<Unix 时间戳>.<原始 Payload>` 计算 HMAC-SHA256，把 `v1=<hex 摘要>` 放入 `X-Relay-Lifeline-Signature`，并把时间戳和 Key ID 分别放入 `X-Relay-Lifeline-Signature-Timestamp`、`X-Relay-Lifeline-Signature-Key-ID`。接收方应检查时间戳新鲜度，按 Key ID 查找 Secret，并使用常量时间比较摘要。轮换时先让接收方在保留旧密钥的同时加入新 Key ID/Secret，再更新发送方环境变量并重启；确认投递已使用新 Key ID 后，才能移除接收方旧密钥。Secret 不进入 YAML、管理 API、日志或诊断包。

持续任务接受 `maxTokens`。只有存在上游权威 `usage.total_tokens` 时才会执行上限；缺少 usage 会以 `usage_missing` 暂停任务，不做 Token 或费用估算。累计权威值达到上限后，当前正在执行的一轮结束，任务随即到期。

- 请求与事故时间线位于 `/var/lib/relay-lifeline/events` 持久卷，按保留期每小时原子压实，服务重启后恢复；未完成请求只恢复为 `orphaned`，不会自动重放。
- 指标、运行事件和实时运行日志位于内存，服务重启后清空。
- 加密捕获位于持久卷，默认保留 72 小时。
- 临时响应缓存权限为 `0600`，交付或失败后删除。
- 诊断包不含正文；完整原文只通过显式确认的流式 ZIP 下载。
