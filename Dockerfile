# NOTE: this builder must provide Go >= the `go` directive in go.mod (1.26,
# set by k8s.io/cri-api). A builder with an older toolchain either silently
# downloads one — defeating -trimpath's reproducibility — or hard-fails under
# GOTOOLCHAIN=local. Pin this to a concrete tag once you have confirmed one
# that satisfies that minimum; :latest is used here only because the tag list
# could not be verified offline.
FROM registry.access.redhat.com/ubi9/go-toolset:latest AS build

ARG VERSION=dev
ARG REVISION=unknown
# Populated by the builder only when --platform is in play (see the
# image-multiarch make target). Under a plain `make image` it is empty, and Go
# treats an empty GOARCH as unset and builds for the host — correct here, but
# incidental rather than intentional.
ARG TARGETARCH

USER 0
WORKDIR /src

# Dependencies first so a source-only change does not re-download the module
# cache on every build.
COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ cmd/
COPY internal/ internal/

RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} \
    go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION} -X main.revision=${REVISION}" \
    -o /out/crio-image-exporter ./cmd/crio-image-exporter

FROM registry.access.redhat.com/ubi9/ubi-micro:latest

ARG VERSION=dev
LABEL org.opencontainers.image.title="crio-image-exporter" \
      org.opencontainers.image.description="Prometheus exporter for CRI-O image disk usage" \
      org.opencontainers.image.source="https://github.com/dvirgilad/crio-image-exporter" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.licenses="Apache-2.0"

COPY --from=build /out/crio-image-exporter /usr/local/bin/crio-image-exporter

# Runs as root because crio.sock is root-owned mode 0660. Every other
# privilege is dropped by the pod's securityContext.
USER 0
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/crio-image-exporter"]
