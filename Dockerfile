# syntax=docker/dockerfile:1
# tellurion-data · 数据面服务（ADR-036 G0）
FROM golang:1.26.11-alpine AS builder

WORKDIR /src
RUN apk add --no-cache git

COPY go.mod go.sum* ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_TIME=unknown
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath \
    -ldflags "-s -w -X main.Version=${VERSION} -X main.Commit=${COMMIT} -X main.BuildTime=${BUILD_TIME}" \
    -o /out/data-service ./cmd/dataservice

FROM alpine:3.19

WORKDIR /app
RUN apk add --no-cache ca-certificates curl tzdata && \
    cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime && \
    echo "Asia/Shanghai" > /etc/timezone && \
    rm -rf /var/cache/apk/*

COPY --from=builder /out/data-service /app/data-service

EXPOSE 8081
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD curl -fsS http://127.0.0.1:8081/health || exit 1

USER nobody
ENTRYPOINT ["/app/data-service"]
