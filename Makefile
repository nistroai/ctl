GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)

BINARY = dist/nistro-$(GOOS)-$(GOARCH)
ifeq ($(GOOS),windows)
BINARY := $(BINARY).exe
endif

.PHONY: build
build:
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -o $(BINARY) .
