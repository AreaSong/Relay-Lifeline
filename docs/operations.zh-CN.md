# Transfer Lifeline 运维手册

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

以下命令应针对已停止的实例或文件副本执行。`-recovery-check` 只读；`-config-migrate` 会先生成权限为 `0600` 的备份，再原子写入 schema 2。

```bash
relay-lifeline -config /etc/relay-lifeline/config.yaml -config-validate
relay-lifeline -config /etc/relay-lifeline/config.yaml -config-migrate
relay-lifeline -config /etc/relay-lifeline/config.yaml -recovery-check
relay-lifeline -journal-verify /var/lib/relay-lifeline/events/requests.jsonl
```

隔离演练重试链时，在测试端口启动 `fault-upstream`，并让一份临时 Lifeline 配置指向它。不要为了演练修改生产 CPA 上游地址。

```bash
go run ./cmd/fault-upstream -listen 127.0.0.1:18317 \
  -sequence 401,429,503,invalid-json,truncated-sse,success
./scripts/ci-integration.sh
```

诊断 ZIP 新增 `recovery-check.json`、`journal-summary.json` 和 `config-backups.json`。备份记录只包含文件名、修改时间、大小、SHA-256、源 schema 和校验状态；诊断包绝不包含原始请求体、响应体或备份正文。

CPA 与 Transfer Lifeline 位于同一个 Compose 网络时，上游使用服务名：

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
3. 检查 CPA 自身状态；Transfer Lifeline 不管理账号池、额度或模型路由。
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

## 重启与排空

Compose 的 `stop_grace_period` 应大于 `server.shutdown-timeout`。收到退出信号后，服务停止接收新连接、把等待中的请求唤醒为一次立即重试，并同步等待活动 Handler 排空。

强制终止进程无法保留旧 TCP 连接。OpenAI-compatible 协议没有让新进程接管旧连接的请求句柄；此时客户端必须重连。未完成捕获会安全标记为 `interrupted`，不会冒充成功。

## 升级

```bash
docker image tag ghcr.io/areasong/transfer-lifeline:CURRENT transfer-lifeline:rollback
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

旧版本可能拒绝新字段，因此不能只回滚镜像而保留更新后的配置。配置保存操作会创建最多 10 份 `0600` 回滚副本。

## 数据与保留

- 请求与事故时间线位于 `/var/lib/relay-lifeline/events` 持久卷，按保留期每小时原子压实，服务重启后恢复；未完成请求只恢复为 `orphaned`，不会自动重放。
- 指标、运行事件和实时运行日志位于内存，服务重启后清空。
- 加密捕获位于持久卷，默认保留 72 小时。
- 临时响应缓存权限为 `0600`，交付或失败后删除。
- 诊断包不含正文；完整原文只通过显式确认的流式 ZIP 下载。
