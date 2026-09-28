# A generic image for pure-Go services that don't need the Python fill-matching engine:
# cmd/executor, cmd/signal-engine, cmd/backtest-runner, and cmd/agent-service are all the
# same "compile a static binary and run it" shape -- one Dockerfile plus a build arg tells
# them apart, instead of opening a near-identical file for each service.
# cmd/webui needs the extra Python fill-matching subprocess and gets its own
# Dockerfile.webui -- see the comment at the top of that file. The two have genuinely
# different runtime dependencies and shouldn't be forced into the same abstraction.
#
# Usage:
#   docker build -f go.Dockerfile --build-arg CMD=executor -t tradeforge-executor .
#   docker build -f go.Dockerfile --build-arg CMD=signal-engine -t tradeforge-signal-engine .

# syntax=docker/dockerfile:1
ARG GO_VERSION=1.26.5

FROM golang:${GO_VERSION}-alpine AS build
ARG CMD
WORKDIR /src

# Dependency layer cached separately: as long as go.mod/go.sum don't change, editing
# business code won't trigger a re-download of dependencies.
COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
COPY pkg ./pkg

# segmentio/kafka-go, pgx/v5, and go-redis are all pure-Go implementations that don't need
# CGO -- the statically-linked binary can go straight into a minimal runtime image with no
# libc.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/${CMD}

FROM alpine:3.20
# ca-certificates: needed to reach exchange/OKX and other external HTTPS endpoints.
# tzdata: needed wherever log timestamps or candle-alignment logic does a local timezone
# conversion -- missing it degrades to UTC-only rather than erroring, but it's best to
# have it installed.
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/app /usr/local/bin/app

ENTRYPOINT ["/usr/local/bin/app"]
