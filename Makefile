BINARY := crio-image-exporter
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
REVISION ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.revision=$(REVISION)

.PHONY: build test vet helm-lint clean

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

IMAGE ?= ghcr.io/dvirgilad/crio-image-exporter
PLATFORMS ?= linux/amd64,linux/arm64

.PHONY: image image-multiarch

image:
	podman build -f Containerfile \
		--build-arg VERSION=$(VERSION) --build-arg REVISION=$(REVISION) \
		-t $(IMAGE):$(VERSION) .

image-multiarch:
	podman build -f Containerfile --platform $(PLATFORMS) --manifest $(IMAGE):$(VERSION) \
		--build-arg VERSION=$(VERSION) --build-arg REVISION=$(REVISION) .

# Targets used by CI. docker-build/docker-push take IMG=<full ref> to match the
# convention the workflows use; they build the same Containerfile as `image`.
.PHONY: docker-build docker-push deadcode

IMG ?= $(IMAGE):$(VERSION)

docker-build:
	docker build -f Containerfile \
		--build-arg VERSION=$(VERSION) --build-arg REVISION=$(REVISION) \
		-t $(IMG) .

docker-push:
	docker push $(IMG)

# Reports unreachable functions. Installed on demand so the repo needs no
# vendored tooling.
deadcode:
	go run golang.org/x/tools/cmd/deadcode@latest -test ./...
