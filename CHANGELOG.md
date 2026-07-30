# Changelog

All notable changes are documented here. This project follows Semantic Versioning.

## [2.2.0] - 2026-07-30

### Added

- Add local Source Han Sans CN, Source Sans 3, and Source Code Pro variable fonts with bundled OFL license texts.
- Add global search, notification center, workspace inspector, confirmation dialogs, and keyboard-accessible focused chart views.
- Add an interactive Three.js request topology with selection focus, bounded rendering, and a mobile/WebGL fallback.

### Changed

- Rebuild the application shell around grouped navigation, a shared workspace header, consistent design tokens, and responsive role-aware controls.
- Reorganize the overview into four KPI summaries and a stable 12-column, 8+4 operating layout with a persistent inspector on ultra-wide screens.
- Improve logs, incidents, captures, and settings with direct search targeting, pause buffering, configuration review sections, and safer unsaved-change handling.
- Cache fingerprinted frontend assets immutably while requiring the HTML shell and font license files to revalidate.

### Fixed

- Prevent browser history navigation from bypassing unsaved configuration confirmation.
- Restore saved settings correctly when changes are discarded and parse Go duration values consistently in capture details.
- Correct desktop and mobile overflow, focus restoration, dialog semantics, text contrast, and Viewer navigation permissions.

## [2.1.1] - 2026-07-29

### Fixed

- Allow ECharts to apply runtime style attributes under the management UI Content Security Policy while keeping inline scripts blocked.
- Preserve a manually selected secondary chart when live incident state changes its recommended default.

## [2.1.0] - 2026-07-29

### Added

- Incident-first overview that prioritizes active incidents, alerts, and recovering requests in one compact operating surface.
- Switchable 15-minute, 1-hour, 6-hour, and 24-hour telemetry windows, secondary chart tabs, and focused chart expansion.
- Collapsible desktop navigation with persistent preference and responsive controls for compact screens.

### Changed

- Rebuilt the management console visual system around denser operational hierarchy, standardized Lucide icons, and responsive layouts tuned for 2560x1440, 1512x982, and mobile viewports.
- Reorganized reliability, pressure, error, and recovery charts into a consistent visualization system with complete/partial-window state.

### Fixed

- Preserve FIFO ordering in the concurrency limiter by giving each queued waiter a unique non-zero-sized address.
- Report live requesting pressure separately from total active requests in pressure charts.

## [2.0.0] - 2026-07-28

### Added

- Persistent request and incident journal health, replay, size, and compaction metrics, with persistence-aware readiness.
- Configuration migration and read-only recovery-check commands, plus a deterministic fault-upstream drill utility.
- Diagnostic ZIP recovery evidence, journal summaries, and configuration-backup fingerprints without raw bodies or configuration contents.
- CI gates for localization, CLI migration/recovery, multi-failure retry recovery, and container health/readiness contracts.

### Changed

- Distinguish an absent management session, an invalid management key, and an expired or restarted session in both the Admin API and Web UI.
- Configuration schema 2 is the release contract; schema 1 remains explicitly migratable.

### Security

- Diagnostic manifests explicitly declare `containsRawBodies: false`; backup exports contain only filename, time, size, SHA-256, schema, and validity metadata.
- Download endpoints now preserve structured authentication error codes instead of collapsing every `401` into one client-side state.

## [1.0.1] - 2026-07-27

### Fixed

- Clear an expired or rotated management key after any management API `401` response and return to the sign-in screen instead of polling indefinitely.
- Ignore delayed unauthorized responses from an older session after a new management key has already been accepted.
- Show an explicit bilingual sign-in message when the saved management key is no longer valid.

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

- Public branding and the official image use Relay-Lifeline and `ghcr.io/areasong/relay-lifeline`.
- Existing `relay-lifeline` binary names, Go module, environment variables, headers, storage paths, and client provider identifiers remain supported.

### Security

- Management and capture keys are separated.
- Authorization and authentication headers are never persisted in capture storage.
- Raw capture access requires Sensitive Data permission and explicit confirmation.
- Capture bodies use chunked AES-256-GCM with a distinct wrapped data key per record.
