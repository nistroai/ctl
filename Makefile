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

.PHONY: build test test-binary test-readme smoke-test canary-test
build:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) .

test:
	go test -count=1 ./...

# Builds once in TestMain unless NISTRO_TEST_BINARY selects an existing binary.
test-binary:
	go test -count=1 -timeout 10m ./test/integration_test/...

# Executes the README examples and its documented auth/config behavior.
test-readme:
	go test -v -count=1 -timeout 10m ./test/integration_test/... -run '^TestREADMECommands$$'

# Requires NISTRO_TEST_BINARY and a smoke account. Never builds from source.
smoke-test:
	ENABLE_SMOKE_TEST=true go test -v -count=1 -timeout 15m ./test/smoke_test/... -run '^TestProductionSmoke$$'

# Four 30m lifecycles plus independent cleanup fit within the overall deadline.
canary-test:
	ENABLE_CANARY_TEST=true go test -v -count=1 -timeout 150m ./test/smoke_test/... -run '^TestProductionCanary$$'
