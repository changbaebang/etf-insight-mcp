BINARY := etf-insight-mcp
PKG    := ./cmd/$(BINARY)

.PHONY: build install run test lint vet tidy clean

build:
	go build -o bin/$(BINARY) $(PKG)

# Installs to $(go env GOPATH)/bin, usually ~/go/bin.
install:
	go install $(PKG)

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
