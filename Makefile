.PHONY: build web test race check docker-build

ifeq ($(shell uname -s),Darwin)
GO_TEST := go test -ldflags=-linkmode=external
GO_BUILD := go build -ldflags=-linkmode=external
GO_RACE := go test -race -ldflags=-linkmode=external
else
GO_TEST := CGO_ENABLED=0 go test
GO_BUILD := CGO_ENABLED=0 go build
GO_RACE := go test -race
endif

web:
	cd web && npm ci && npm run build

build: web
	$(GO_BUILD) -trimpath -o relay-lifeline ./cmd/relay-lifeline

test:
	$(GO_TEST) ./...

race:
	$(GO_RACE) -count=1 ./...

check:
	cd web && npm ci && npm run l10n:check && npm run typecheck && npm run build
	$(GO_TEST) ./...
	CGO_ENABLED=0 go vet ./...

docker-build:
	docker build -t relay-lifeline:dev .
