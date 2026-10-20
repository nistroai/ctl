GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE ?= $(shell date -u +'%Y-%m-%dT%H:%M:%SZ')

BINARY = dist/nistro-$(GOOS)-$(GOARCH)
ifeq ($(GOOS),windows)
BINARY := $(BINARY).exe
endif

LDFLAGS = -s -w \
	-X nistro-ctl/internal/buildinfo.Version=$(VERSION) \
	-X nistro-ctl/internal/buildinfo.Commit=$(COMMIT) \
	-X nistro-ctl/internal/buildinfo.Date=$(BUILD_DATE)

.PHONY: build
build:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) .
