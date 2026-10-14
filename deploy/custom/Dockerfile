ARG GO_IMAGE=golang:1.26.4-bookworm
ARG RUNTIME_IMAGE=debian:bookworm-slim
FROM ${GO_IMAGE} AS builder
WORKDIR /app
RUN apt-get update && apt-get install -y --no-install-recommends build-essential git && rm -rf /var/lib/apt/lists/*
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown
RUN CGO_ENABLED=1 GOOS=linux go build -buildvcs=false -ldflags="-s -w -X main.Version=${VERSION} -X main.Commit=${COMMIT} -X main.BuildDate=${BUILD_DATE}" -o /CLIProxyAPI ./cmd/server

FROM ${RUNTIME_IMAGE}
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl python3 python3-yaml tzdata && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 10001 --home-dir /CLIProxyAPI --shell /usr/sbin/nologin cliproxy \
    && mkdir -p /CLIProxyAPI/config /CLIProxyAPI/auths /CLIProxyAPI/logs /CLIProxyAPI/plugins /CLIProxyAPI/static \
    && chown -R 10001:10001 /CLIProxyAPI
WORKDIR /CLIProxyAPI
COPY --from=builder /CLIProxyAPI /CLIProxyAPI/CLIProxyAPI
COPY --from=custom_ui management.html release.json /CLIProxyAPI/static/
COPY --from=custom_ui LICENSE /usr/share/doc/cliproxyapi-ui/LICENSE
COPY LICENSE /usr/share/doc/cliproxyapi/LICENSE
COPY deploy/custom/readiness.py /CLIProxyAPI/deploy/readiness.py
ENV MANAGEMENT_STATIC_PATH=/CLIProxyAPI/static WRITABLE_PATH=/CLIProxyAPI TZ=Asia/Shanghai
USER 10001:10001
EXPOSE 8317
HEALTHCHECK --interval=20s --timeout=5s --start-period=20s --retries=6 CMD curl --fail --silent --noproxy '*' http://127.0.0.1:8317/healthz >/dev/null || exit 1
CMD ["./CLIProxyAPI", "--config", "/CLIProxyAPI/config/config.yaml"]
