# Security Policy

[简体中文](SECURITY.zh-CN.md)

## Reporting a vulnerability

Do not disclose exploitable security issues in a public issue. Contact the repository maintainer through the security channel published by the repository and include reproduction steps, affected versions, impact, and a proposed mitigation when available.

Never include a live API key, admin key, prompt, model response, raw upstream error, or credential-bearing URL in a report. Replace secrets with deterministic placeholders.

## Security boundary

- Relay-Lifeline forwards downstream Authorization to one configured upstream in memory.
- Request bodies, response bodies, and Authorization are not logged by the supported configuration.
- The management API requires a separate `RELAY_LIFELINE_ADMIN_KEY` of at least 24 characters.
- Docker publishes the service on host loopback by default.
- The admin UI and API are not designed for direct public internet exposure.
- Response cache files use mode `0600` and are removed after use.
- Diagnostics and exported bundles redact configured endpoints and omit safe error details.
- Temporary diagnostic captures use a separate `RELAY_LIFELINE_CAPTURE_KEY`, chunked AES-256-GCM, and a distinct data key per capture session.
- Authorization, Cookie, API-key, token, and other authentication headers are never persisted in capture storage.
- Filtered bodies may be previewed online. Full raw content requires explicit confirmation and is decrypted directly into the download stream without a plaintext temporary ZIP.
- Captured bodies are excluded from Webhooks, regular runtime logs, and default diagnostics, and expire after 72 hours by default.

Existing captures cannot be recovered if `RELAY_LIFELINE_CAPTURE_KEY` is lost. The first release does not maintain a historical key ring. Download records that must be retained and delete the rest before rotating the key. Never commit this key alongside the admin key, CPA API key, or image.

Relay credentials, client API keys, request bodies, response bodies, and model data are sensitive. Operators are responsible for TLS and access control when traffic leaves a trusted host or network.
