# Build stage
FROM golang:1.24-bookworm AS builder

WORKDIR /app

ARG GOPRIVATE_ARG
ARG GOPROXY_ARG
ARG GOSUMDB_ARG=off
ARG APK_MIRROR_ARG

ENV GOPRIVATE=${GOPRIVATE_ARG}
ENV GOPROXY=${GOPROXY_ARG}
ENV GOSUMDB=${GOSUMDB_ARG}

RUN sed -i "s@deb.debian.org@${APK_MIRROR_ARG:-mirrors.tuna.tsinghua.edu.cn}@g" /etc/apt/sources.list.d/debian.sources && \
    apt-get update && \
    apt-get install -y git build-essential libsqlite3-dev

RUN go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd/download cmd/download
RUN go run cmd/download/duckdb/duckdb.go
COPY . .

ARG VERSION_ARG
ARG COMMIT_ID_ARG
ARG BUILD_TIME_ARG
ARG GO_VERSION_ARG

ENV VERSION=${VERSION_ARG}
ENV COMMIT_ID=${COMMIT_ID_ARG}
ENV BUILD_TIME=${BUILD_TIME_ARG}
ENV GO_VERSION=${GO_VERSION_ARG}

RUN --mount=type=cache,target=/go/pkg/mod make build-prod
RUN --mount=type=cache,target=/go/pkg/mod cp -r /go/pkg/mod/github.com/yanyiwu/ /app/yanyiwu/

# Final stage
FROM debian:12.12-slim

WORKDIR /app

ARG APK_MIRROR_ARG

RUN useradd -m -s /bin/bash appuser

# Core system packages
RUN sed -i "s@deb.debian.org@${APK_MIRROR_ARG:-mirrors.tuna.tsinghua.edu.cn}@g" /etc/apt/sources.list.d/debian.sources && \
    apt-get update && \
    apt-get install -y --no-install-recommends \
        build-essential postgresql-client default-mysql-client \
        ca-certificates tzdata sed curl bash vim wget \
        libsqlite3-0 \
        python3 python3-pip python3-dev libffi-dev libssl-dev \
        gosu && \
    apt-get clean

# Node.js (separate layer)
RUN sed -i "s@deb.debian.org@${APK_MIRROR_ARG:-mirrors.tuna.tsinghua.edu.cn}@g" /etc/apt/sources.list.d/debian.sources && \
    apt-get update && \
    apt-get install -y --no-install-recommends nodejs npm && \
    apt-get clean

# ffmpeg (large, separate layer)
RUN sed -i "s@deb.debian.org@${APK_MIRROR_ARG:-mirrors.tuna.tsinghua.edu.cn}@g" /etc/apt/sources.list.d/debian.sources && \
    apt-get update && \
    apt-get install -y --no-install-recommends ffmpeg && \
    apt-get clean

# Python tooling via Tsinghua mirror
RUN python3 -m pip install --break-system-packages \
        -i https://pypi.tuna.tsinghua.edu.cn/simple \
        --upgrade pip setuptools wheel uv && \
    mkdir -p /home/appuser/.local/bin && \
    ln -sf /usr/local/bin/uv /home/appuser/.local/bin/uv && \
    ln -sf /usr/local/bin/uvx /home/appuser/.local/bin/uvx && \
    chown -R appuser:appuser /home/appuser && \
    chmod +x /usr/local/bin/uvx

RUN mkdir -p /data/files && \
    chown -R appuser:appuser /app /data/files

COPY --from=builder /go/bin/migrate /usr/local/bin/
COPY --from=builder /app/yanyiwu/ /go/pkg/mod/github.com/yanyiwu/

COPY --from=builder /app/config ./config
COPY --from=builder /app/scripts ./scripts
COPY --from=builder /app/migrations ./migrations
COPY --from=builder /app/dataset/samples ./dataset/samples
COPY --from=builder /app/skills/preloaded ./skills/preloaded
COPY --from=builder /app/skills/preloaded ./skills/_builtin
COPY --from=builder /root/.duckdb /home/appuser/.duckdb
COPY --from=builder /app/WeKnora .

COPY --from=builder /app/scripts/docker-entrypoint.sh ./scripts/docker-entrypoint.sh

RUN chmod +x ./scripts/*.sh

EXPOSE 8080

ENTRYPOINT ["./scripts/docker-entrypoint.sh"]
CMD ["./WeKnora"]