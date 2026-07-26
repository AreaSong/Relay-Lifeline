.PHONY: build web test check docker-build

ifeq ($(shell uname -s),Darwin)
GO_TEST := go test -ldflags=-linkmode=external
GO_BUILD := go build -ldflags=-linkmode=external
else
GO_TEST := CGO_ENABLED=0 go test
GO_BUILD := CGO_ENABLED=0 go build
endif

web:
	cd web && npm ci && npm run build

build: web
	$(GO_BUILD) -trimpath -o relay-lifeline ./cmd/relay-lifeline

test:
	$(GO_TEST) ./...

check:
	cd web && npm ci && npm run l10n:check && npm run typecheck && npm run build
	$(GO_TEST) ./...
	CGO_ENABLED=0 go vet ./...

docker-build:
	docker build -t relay-lifeline:dev .
