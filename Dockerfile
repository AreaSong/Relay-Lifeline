FROM golang:1.22-alpine AS go-builder
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/relay-lifeline ./cmd/relay-lifeline

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata && addgroup -S relay && adduser -S -G relay relay
COPY --from=go-builder /out/relay-lifeline /usr/local/bin/relay-lifeline
WORKDIR /var/lib/relay-lifeline
USER relay
EXPOSE 8318
ENTRYPOINT ["relay-lifeline"]
CMD ["-config", "/etc/relay-lifeline/config.yaml"]
