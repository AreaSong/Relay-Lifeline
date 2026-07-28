# Security Policy

[简体中文](SECURITY.zh-CN.md)

## Reporting a vulnerability

Do not disclose exploitable security issues in a public issue. Use [GitHub private vulnerability reporting](https://github.com/AreaSong/Relay-Lifeline/security/advisories/new) and include reproduction steps, affected versions, impact, and a proposed mitigation when available.

Never include a live API key, admin key, prompt, model response, raw upstream error, or credential-bearing URL in a report. Replace secrets with deterministic placeholders.

## Security boundary

- Relay-Lifeline forwards downstream Authorization to one configured upstream in memory.
- Request bodies, response bodies, and Authorization are not logged by the supported configuration.
- The management API uses distinct role keys of at least 24 characters: optional Viewer, Operator (`RELAY_LIFELINE_ADMIN_KEY`), and Sensitive Data. Viewer is read-only, Operator can mutate runtime state, and only Sensitive Data can download full raw content.
- Docker publishes the service on host loopback by default.
- The admin UI and API are not designed for direct public internet exposure.
- Response cache files use mode `0600` and are removed after use.
- Diagnostics and exported bundles redact configured endpoints and omit safe error details.
- Temporary diagnostic captures use a separate `RELAY_LIFELINE_CAPTURE_KEY`, chunked AES-256-GCM, and a distinct data key per capture session.
- Authorization, Cookie, API-key, token, and other authentication headers are never persisted in capture storage.
- Filtered bodies may be previewed online. Full raw content requires explicit confirmation and is decrypted directly into the download stream without a plaintext temporary ZIP.
- Captured bodies are excluded from Webhooks, regular runtime logs, and default diagnostics, and expire after 72 hours by default.

If a capture key still referenced by a record is lost, that capture cannot be recovered. During rotation, keep the old key and the new active key in the key ring, restart, and rewrap data keys through the management interface. Remove an old key only after the status endpoint reports zero records for that key ID and zero unresolved records. Rewrap changes only `0600` metadata and never decrypts bodies to disk; a write failure rolls back metadata already updated. Never commit these keys alongside management keys, the CPA API key, or an image.

Relay credentials, client API keys, request bodies, response bodies, and model data are sensitive. Operators are responsible for TLS and access control when traffic leaves a trusted host or network.
