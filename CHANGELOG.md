# Changelog

All notable changes are documented here. This project follows Semantic Versioning.

## [1.0.0] - 2026-07-27

### Added

- Retry for all HTTP, transport, protocol, malformed JSON, incomplete SSE, and response-body idle failures.
- Random 60-120 second retry delay, 15-second downstream heartbeat, manual retry, cancellation, queue limits, and recovery pacing.
- Complete-response buffering with bounded memory and mode-`0600` spill files.
- Viewer, Operator, and Sensitive Data management roles.
- Encrypted request/response capture, filtered and raw downloads, 72-hour retention, key rings, and data-key rewrap.
- Request timelines, structured logs, reliability metrics, diagnostics, risk alerts, and Webhook notifications.
- Build identity, versioned configuration, validation plans, backups, graceful drain, and rollback guidance.
- Chinese and English Web UI, API messages, CLI text, logs, diagnostics, and documentation.

### Compatibility

- Public branding and the official image use Transfer Lifeline and `ghcr.io/areasong/transfer-lifeline`.
- Existing `relay-lifeline` binary names, Go module, environment variables, headers, storage paths, and client provider identifiers remain supported.

### Security

- Management and capture keys are separated.
- Authorization and authentication headers are never persisted in capture storage.
- Raw capture access requires Sensitive Data permission and explicit confirmation.
- Capture bodies use chunked AES-256-GCM with a distinct wrapped data key per record.

