// Package cri provides a narrow, runtime-agnostic view of the CRI image and
// runtime services. Only client.go imports k8s.io/cri-api; everything else in
// the exporter depends on the types declared here.
package cri

import (
	"context"
	"time"
)

// Image is a container image present on the node.
type Image struct {
	ID          string
	RepoTags    []string
	RepoDigests []string
	// Size is the apparent size in bytes. It does NOT account for layers
	// shared with other images; see internal/storage for exact attribution.
	Size   uint64
	Pinned bool
}

// Filesystem is usage information for one image filesystem.
type Filesystem struct {
	Mountpoint string
	UsedBytes  uint64
	InodesUsed uint64
}

// Container is a container known to the runtime.
type Container struct {
	ID string
	// ImageID is the node-local identifier of the image this container runs.
	// CRI defines it as "the unique identifier of the image on the node", which
	// must match Image.ID. It may be empty on older runtimes, which is why
	// ImageRef exists as a fallback.
	ImageID string
	// ImageRef is a DIGESTED reference to the image, e.g.
	// "quay.io/foo/bar@sha256:...". It matches an entry of Image.RepoDigests,
	// NOT Image.ID. Comparing it against Image.ID never succeeds.
	ImageRef string
	// State is the lowercase CRI state: created, running, exited, unknown.
	State string
}

// RuntimeInfo identifies the container runtime.
type RuntimeInfo struct {
	Name       string
	Version    string
	APIVersion string
}

// Client is the exporter's view of CRI-O. All methods are safe for concurrent
// use. Implementations must not retain the passed context beyond the call.
type Client interface {
	ListImages(ctx context.Context) ([]Image, error)
	ImageFsInfo(ctx context.Context) ([]Filesystem, error)
	ListContainers(ctx context.Context) ([]Container, error)
	Version(ctx context.Context) (RuntimeInfo, error)
	// ImageCreated returns the image's creation time. It is significantly more
	// expensive than the other calls: it issues ImageStatus with verbose output
	// and parses a runtime-specific blob.
	ImageCreated(ctx context.Context, imageID string) (time.Time, error)
	Close() error
}
