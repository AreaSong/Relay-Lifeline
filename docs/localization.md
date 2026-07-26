# Localization

[简体中文](localization.zh-CN.md)

Relay-Lifeline supports `zh-CN` and `en-US` across the Web UI, management API, CLI, diagnostics, logs, Webhooks, history, timelines, alerts, and configuration validation.

## Locale ownership

| Surface | Locale source | Runtime behavior |
| --- | --- | --- |
| Web UI | `localStorage`, then browser language | Changes immediately |
| Management API | `Accept-Language`, then configured default | Per request; returns `Content-Language` |
| Proxy error body | Request `Accept-Language`, then configured default | Per request |
| CLI | `--locale`, then `LANG`, then configured default | Resolved at startup |
| Structured logs | `logging.locale` | Read for each log event |
| Webhooks | `notifications.locale` | Read when each event is queued |

Unsupported or malformed request languages fall back to `localization.default-locale`; missing translations fall back to `localization.fallback-locale`.

## Stable and localized data

JSON field names, HTTP-independent error codes, event codes, status values, diagnostic check IDs, alert types, and message codes are stable English identifiers. They must not be translated.

Human-readable `message`, `error`, labels, descriptions, command help, and log text are localized. Long-lived state stores `messageCode` and `messageDetails`, then renders `message` for the requested locale. Do not persist only the rendered text for a new event.

## Catalog locations

- Backend: `internal/l10n/locales/active.en-US.json` and `active.zh-CN.json`
- Frontend: `web/src/locales/{en-US,zh-CN}/<namespace>.json`
- Runtime setup: `internal/l10n/localizer.go` and `web/src/i18n/index.ts`

Both locales must contain the same namespaces, keys, non-empty translations, and interpolation parameters. English plural variants and the Chinese base key are intentionally mirrored so the catalogs remain structurally equal.

## Adding text

1. Choose an existing namespace or create the same namespace in both frontend locale directories.
2. Add the same key and interpolation parameters to both languages.
3. For backend state, introduce a stable message ID and store details instead of pre-rendered text.
4. Add or update a behavior test when locale selection, fallback, stored state, logs, or Webhooks are affected.
5. Run:

```bash
cd web
npm run l10n:check
npm run typecheck
npm run build
```

The localization check is also part of `make check`. It validates frontend namespace/key parity and backend ID parity, duplicate IDs, empty translations, and interpolation parameter equality.

## Interpolation

Frontend catalogs use i18next syntax such as `{{count}}`. Backend catalogs use go-i18n template data such as `{{.Status}}`. Parameter names are contracts: changing one requires changing the producer and both translations together.

Never place secrets, request bodies, response bodies, Authorization, or raw upstream data into localization details. Message details can reach logs, APIs, histories, diagnostics, or Webhooks.
