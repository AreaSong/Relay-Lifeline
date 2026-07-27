# 本地化说明

[English](localization.md)

Transfer Lifeline 的 Web UI、管理 API、CLI、诊断、日志、Webhook、历史、时间线、告警和配置校验均支持 `zh-CN` 与 `en-US`。

## 语言来源

| 使用面 | 语言来源 | 运行行为 |
| --- | --- | --- |
| Web UI | `localStorage`，其次浏览器语言 | 立即切换 |
| 管理 API | `Accept-Language`，其次配置默认语言 | 每个请求决定；返回 `Content-Language` |
| 代理错误响应 | 请求 `Accept-Language`，其次配置默认语言 | 每个请求决定 |
| CLI | `--locale`，其次 `LANG`，最后配置默认语言 | 启动时决定 |
| 结构化日志 | `logging.locale` | 每条日志生成时读取 |
| Webhook | `notifications.locale` | 每个事件入队时读取 |

不支持或格式错误的请求语言回退到 `localization.default-locale`；缺失翻译回退到 `localization.fallback-locale`。

## 稳定数据与本地化数据

JSON 字段、与 HTTP 无关的错误代码、事件代码、状态值、诊断检查 ID、告警类型和消息代码都是稳定英文标识，不得翻译。

面向人的 `message`、`error`、标签、说明、命令帮助和日志正文需要翻译。长期状态应保存 `messageCode` 与 `messageDetails`，再按请求语言生成 `message`。新增事件不能只持久化已经渲染的文字。

## 词典位置

- 后端：`internal/l10n/locales/active.en-US.json` 与 `active.zh-CN.json`
- 前端：`web/src/locales/{en-US,zh-CN}/<namespace>.json`
- 运行时初始化：`internal/l10n/localizer.go` 与 `web/src/i18n/index.ts`

两种语言必须拥有相同命名空间、相同键、非空翻译和相同插值参数。英文复数变体与中文基础键会互相补齐，从而保持词典结构严格一致。

## 新增文字

1. 使用现有命名空间，或在两个前端语言目录中同时创建同名命名空间。
2. 为两种语言加入相同键和插值参数。
3. 后端状态使用稳定消息 ID，并保存详情，不提前固化渲染文本。
4. 语言选择、回退、存储状态、日志或 Webhook 发生变化时补充行为测试。
5. 运行：

```bash
cd web
npm run l10n:check
npm run typecheck
npm run build
```

翻译检查也已加入 `make check`。它会校验前端命名空间和键一致、后端 ID 一致、重复 ID、空翻译以及插值参数一致性。

## 插值

前端词典使用 i18next 语法，例如 `{{count}}`；后端词典使用 go-i18n 模板数据，例如 `{{.Status}}`。参数名属于契约，修改时必须同步修改生产代码和两种翻译。

不得把密钥、请求体、响应体、Authorization 或原始上游数据放入翻译详情。消息详情可能进入日志、API、历史、诊断或 Webhook。
