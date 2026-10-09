BINARY  := etf-insight-mcp
PKG     := ./cmd/$(BINARY)
# Version from the nearest tag, or the short commit plus -dirty when the
# tree has changes; override with: make build VERSION=1.2.3
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: build install run test lint vet tidy clean

build:
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY) $(PKG)

# Installs to $(go env GOPATH)/bin, usually ~/go/bin.
install:
	go install -ldflags "$(LDFLAGS)" $(PKG)

run: build
	./bin/$(BINARY)

test:
	go test -race -cover ./...

vet:
	go vet ./...

lint:
	golangci-lint run ./...

tidy:
	go mod tidy

clean:
	rm -rf bin dist
