package cri

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Fake is an in-memory Client for tests. The zero value is usable and returns
// empty results. Errors are injected per method so tests can exercise partial
// failure, which is the exporter's most important behavior.
type Fake struct {
	Images      []Image
	Filesystems []Filesystem
	Containers  []Container
	Runtime     RuntimeInfo
	Created     map[string]time.Time

	ListImagesErr     error
	ImageFsInfoErr    error
	ListContainersErr error
	VersionErr        error
	ImageCreatedErr   error

	mu           sync.Mutex
	createdCalls map[string]int
}

func (f *Fake) ListImages(context.Context) ([]Image, error) {
	if f.ListImagesErr != nil {
		return nil, f.ListImagesErr
	}
	return f.Images, nil
}

func (f *Fake) ImageFsInfo(context.Context) ([]Filesystem, error) {
	if f.ImageFsInfoErr != nil {
		return nil, f.ImageFsInfoErr
	}
	return f.Filesystems, nil
}

func (f *Fake) ListContainers(context.Context) ([]Container, error) {
	if f.ListContainersErr != nil {
		return nil, f.ListContainersErr
	}
	return f.Containers, nil
}

func (f *Fake) Version(context.Context) (RuntimeInfo, error) {
	if f.VersionErr != nil {
		return RuntimeInfo{}, f.VersionErr
	}
	return f.Runtime, nil
}

func (f *Fake) ImageCreated(_ context.Context, imageID string) (time.Time, error) {
	f.mu.Lock()
	if f.createdCalls == nil {
		f.createdCalls = map[string]int{}
	}
	f.createdCalls[imageID]++
	f.mu.Unlock()

	if f.ImageCreatedErr != nil {
		return time.Time{}, f.ImageCreatedErr
	}
	ts, ok := f.Created[imageID]
	if !ok {
		return time.Time{}, fmt.Errorf("fake: no creation time for image %s", imageID)
	}
	return ts, nil
}

func (f *Fake) Close() error { return nil }

// ImageCreatedCalls reports how many times ImageCreated was called for an
// image. The age cache test uses this to prove it does not re-query.
func (f *Fake) ImageCreatedCalls(imageID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.createdCalls[imageID]
}

var _ Client = (*Fake)(nil)
