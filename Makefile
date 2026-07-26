.PHONY: build web test check docker-build

web:
	cd web && npm ci && npm run build

build: web
	CGO_ENABLED=0 go build -trimpath -o relay-lifeline ./cmd/relay-lifeline

test:
	CGO_ENABLED=0 go test ./...

check:
	cd web && npm ci && npm run l10n:check && npm run typecheck && npm run build
	CGO_ENABLED=0 go test ./...
	CGO_ENABLED=0 go vet ./...

docker-build:
	docker build -t relay-lifeline:dev .
