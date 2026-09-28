# 给不需要 Python 撮合引擎的纯 Go 服务用的通用镜像：cmd/executor、cmd/signal-engine、
# cmd/backtest-runner、cmd/agent-service 都是同样的"编译一个静态二进制、跑起来"，用一份
# Dockerfile + 一个 build arg 区分，不为每个服务各开一份近乎相同的文件。
# cmd/webui 需要额外的 Python 撮合引擎子进程，用独立的 Dockerfile.webui，见那份文件顶部的
# 注释——两者的运行时依赖真的不一样，不该硬塞进同一个抽象里。
#
# 用法：
#   docker build -f go.Dockerfile --build-arg CMD=executor -t tradeforge-executor .
#   docker build -f go.Dockerfile --build-arg CMD=signal-engine -t tradeforge-signal-engine .

# syntax=docker/dockerfile:1
ARG GO_VERSION=1.26.5

FROM golang:${GO_VERSION}-alpine AS build
ARG CMD
WORKDIR /src

# 依赖层单独缓存：go.mod/go.sum 不变时，改业务代码不会触发重新下载依赖。
COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
COPY pkg ./pkg

# segmentio/kafka-go、pgx/v5、go-redis 都是纯 Go 实现，不需要 CGO——静态链接的二进制
# 可以直接放进一个没有 libc 的极简运行时镜像。
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/${CMD}

FROM alpine:3.20
# ca-certificates：连交易所/OKX 等外部 HTTPS 接口需要；tzdata：日志时间戳、K 线时间对齐
# 相关逻辑用到本地时区转换时需要，缺了会退化成 UTC-only，不是报错，但最好装上。
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/app /usr/local/bin/app

ENTRYPOINT ["/usr/local/bin/app"]
