BINARY := crio-image-exporter
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
REVISION ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.revision=$(REVISION)

.PHONY: build test vet lint helm-lint clean

build:
	CGO_ENABLED=0 go build -ldflags '$(LDFLAGS)' -o bin/$(BINARY) ./cmd/crio-image-exporter

test:
	go test ./... -race -count=1

vet:
	go vet ./...

helm-lint:
	helm lint charts/crio-image-exporter

clean:
	rm -rf bin/
