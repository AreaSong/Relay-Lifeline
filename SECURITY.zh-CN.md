# 安全策略

[English](SECURITY.md)

## 报告漏洞

请勿在公开 Issue 中披露可利用的安全问题。使用 [GitHub 私密安全报告](https://github.com/AreaSong/Relay-Lifeline/security/advisories/new) 联系维护者，并尽量提供复现步骤、受影响版本、影响范围和建议缓解方式。

报告中不得包含真实 API Key、管理密钥、提示词、模型响应、原始上游错误或含凭据 URL。请使用可辨识但无效的固定占位符替换。

## 安全边界

- Relay-Lifeline 只在内存中把下游 Authorization 透传给唯一配置上游。
- 支持的配置不记录请求体、响应体或 Authorization。
- 管理 API 使用彼此不同且不少于 24 个字符的角色密钥：可选 Viewer、Operator（`RELAY_LIFELINE_ADMIN_KEY`）和 Sensitive Data。Viewer 只读，Operator 可以修改运行状态，只有 Sensitive Data 可以下载完整原文。
- Docker 默认只将服务发布到宿主机回环地址。
- 管理 UI 和 API 不应直接暴露到公网。
- 响应缓存文件权限为 `0600`，使用后删除。
- 诊断和导出包会遮蔽配置端点并排除安全错误详情。
- 临时诊断捕获使用独立 `RELAY_LIFELINE_CAPTURE_KEY` 和分块 AES-256-GCM；每个捕获会话使用独立数据密钥。
- Authorization、Cookie、API Key、Token 和其他认证 Header 不进入捕获存储。
- 过滤正文可以在线预览；完整原文只能经二次确认下载，解密过程不产生明文临时 ZIP。
- 捕获正文不会进入 Webhook、普通运行日志或默认诊断包，默认 72 小时后删除。
- 已配置 Webhook 时必须启用签名并 fail-closed：`RELAY_LIFELINE_WEBHOOK_SIGNING_SECRET` 永不序列化到配置或 API 响应；缺失、部分配置或过短 Secret 会在启动阶段被拒绝。
- 持续任务的严格 Token 上限只累计上游 `usage.total_tokens`；缺少 usage 会暂停任务，不使用 Token 或费用估算。

任何仍被记录引用的捕获密钥丢失后，对应捕获都无法恢复。轮换时必须将旧密钥和新活动密钥同时放入 Key Ring，重启后通过管理端执行数据密钥重包裹；只有状态接口显示旧 Key ID 记录数为零且未解析记录为零后，才能移除旧密钥。重包裹只修改 `0600` 元数据，不会把正文解密到磁盘；写入失败会回滚已更新的元数据。不要将密钥与管理密钥、CPA API Key 或镜像一起提交到仓库。

中转站凭据、客户端 API Key、请求体、响应体和模型数据都属于敏感信息。流量离开可信主机或网络时，运营者负责配置 TLS 和访问控制。

Webhook 接收方必须使用精确的原始请求体，按 `<Unix 时间戳>.<原始 Payload>` 计算 HMAC-SHA256 并拒绝过期时间戳。发送方会在独立 Header 中提供签名版本（`v1`）、时间戳和 Key ID。轮换时依次执行：接收方先加入新密钥、发送方再切换环境变量并重启、确认新 Key ID 生效、最后移除接收方旧密钥。不要把 Secret 放入源码、YAML、日志、诊断包或管理 UI 状态。
