FROM golang:1.25-alpine AS go-builder
ARG VERSION=dev
ARG REVISION=unknown
ARG BUILD_TIME=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X main.version=${VERSION} -X main.revision=${REVISION} -X main.builtAt=${BUILD_TIME}" -o /out/relay-lifeline ./cmd/relay-lifeline

FROM alpine:3.21
ARG VERSION=dev
ARG REVISION=unknown
ARG BUILD_TIME=unknown
LABEL org.opencontainers.image.title="Relay-Lifeline" \
      org.opencontainers.image.source="https://github.com/AreaSong/Relay-Lifeline" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.created="${BUILD_TIME}"
RUN apk add --no-cache ca-certificates tzdata && addgroup -S relay && adduser -S -G relay relay
COPY --from=go-builder /out/relay-lifeline /usr/local/bin/relay-lifeline
WORKDIR /var/lib/relay-lifeline
USER relay
EXPOSE 8318
HEALTHCHECK --interval=15s --timeout=3s --start-period=5s --retries=3 CMD wget -q -O - http://127.0.0.1:8318/healthz || exit 1
ENTRYPOINT ["relay-lifeline"]
CMD ["-config", "/etc/relay-lifeline/config.yaml"]
