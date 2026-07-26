# Contributing

[简体中文](CONTRIBUTING.zh-CN.md)

## Development environment

- Go 1.22 or newer
- Node.js 22 or newer
- Docker for image and integration checks

Run the complete local gate before submitting a change:

```bash
make check
```

On a recent macOS/Xcode combination, use external linking if Go 1.22 reports a missing `LC_UUID` load command:

```bash
go test -ldflags=-linkmode=external ./...
```

## Change requirements

- Add tests proportional to the changed behavior.
- Protocol changes must cover success markers, error events, interrupted streams, cancellation, and sensitive-header forwarding.
- User-visible text must be added to both `zh-CN` and `en-US` catalogs.
- Stable JSON keys, event codes, message codes, and status values must remain English.
- Do not add real API keys, prompts, responses, or unredacted upstream errors to code, tests, issues, screenshots, or commits.
- Keep the `relay-lifeline` technical identifiers compatible unless a migration is explicitly designed.

The localization gate runs through `npm run l10n:check` and is included in `make check`. See [Localization](docs/localization.md).
