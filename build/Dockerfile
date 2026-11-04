# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine AS builder

RUN apk add --no-cache git

WORKDIR /src

ARG TARGETOS
ARG TARGETARCH
ARG GOPROXY=https://proxy.golang.org,direct
ARG GOSUMDB=sum.golang.org
ARG GIT_COMMIT=unknown
ARG GIT_VERSION=dev

ENV CGO_ENABLED=0 \
    GOOS=${TARGETOS} \
    GOARCH=${TARGETARCH} \
    GOPROXY=${GOPROXY} \
    GOSUMDB=${GOSUMDB}

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked \
    go mod download

RUN --mount=type=bind,source=.,target=/src,rw \
    --mount=type=cache,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,target=/root/.cache/go-build,id=gobuild-${TARGETOS}-${TARGETARCH} \
    BUILD_DATE=$(date -u +'%Y-%m-%dT%H:%M:%SZ') && \
    go build -trimpath \
      -ldflags "-s -w \
        -X nistro-ctl/internal/buildinfo.Version=${GIT_VERSION} \
        -X nistro-ctl/internal/buildinfo.Commit=${GIT_COMMIT} \
        -X nistro-ctl/internal/buildinfo.Date=${BUILD_DATE}" \
      -o /out/nistro .

FROM alpine:latest AS app

RUN apk --no-cache upgrade && \
    apk add --no-cache ca-certificates && \
    addgroup -S nistro && \
    adduser -S -G nistro nistro

COPY --from=builder /out/nistro /usr/local/bin/nistro

USER nistro
WORKDIR /home/nistro
ENTRYPOINT ["/usr/local/bin/nistro"]
