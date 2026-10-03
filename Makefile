BIN     := bin/coralctl
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build run demo test vet fmt check install clean

all: build

build:
	go build -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/coralctl

## run: build and start against the current kubeconfig
run: build
	./$(BIN)

## demo: build and start with the fake demo clusters
demo: build
	./$(BIN) --demo

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w cmd internal

## check: formatting, vet and tests, as before a commit
check:
	@test -z "$$(gofmt -l cmd internal)" || { gofmt -l cmd internal; echo "run 'make fmt'"; exit 1; }
	go vet ./...
	go test ./...

install:
	go install -ldflags '$(LDFLAGS)' ./cmd/coralctl

clean:
	rm -rf bin
