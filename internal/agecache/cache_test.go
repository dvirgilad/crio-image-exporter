package agecache

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/dvirgilad/crio-image-exporter/internal/cri"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestRefreshPopulatesCache(t *testing.T) {
	f := &cri.Fake{
		Images:  []cri.Image{{ID: "sha256:aaa"}, {ID: "sha256:bbb"}},
		Created: map[string]time.Time{"sha256:aaa": time.Unix(1000, 0), "sha256:bbb": time.Unix(2000, 0)},
	}
	c := New(f, time.Minute, quietLogger())

	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	got, ok := c.Get("sha256:aaa")
	if !ok || !got.Equal(time.Unix(1000, 0)) {
		t.Fatalf("Get = %v, %v", got, ok)
	}
	if c.Len() != 2 {
		t.Errorf("Len = %d, want 2", c.Len())
	}
	if c.LastRefresh().IsZero() {
		t.Error("LastRefresh should be set after a successful refresh")
	}
}

// Creation time is immutable per image ID, so a second refresh must issue no
// ImageStatus calls for images it already knows. This is what makes the cache
// cheap in steady state.
func TestRefreshDoesNotRequeryKnownImages(t *testing.T) {
	f := &cri.Fake{
		Images:  []cri.Image{{ID: "sha256:aaa"}},
		Created: map[string]time.Time{"sha256:aaa": time.Unix(1000, 0)},
	}
	c := New(f, time.Minute, quietLogger())

	for i := 0; i < 3; i++ {
		if err := c.Refresh(context.Background()); err != nil {
			t.Fatalf("Refresh %d: %v", i, err)
		}
	}
	if got := f.ImageCreatedCalls("sha256:aaa"); got != 1 {
		t.Errorf("ImageCreated calls = %d, want 1", got)
	}
}

func TestRefreshPrunesRemovedImages(t *testing.T) {
	f := &cri.Fake{
		Images:  []cri.Image{{ID: "sha256:aaa"}, {ID: "sha256:bbb"}},
		Created: map[string]time.Time{"sha256:aaa": time.Unix(1000, 0), "sha256:bbb": time.Unix(2000, 0)},
	}
	c := New(f, time.Minute, quietLogger())
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	f.Images = []cri.Image{{ID: "sha256:aaa"}}
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if _, ok := c.Get("sha256:bbb"); ok {
		t.Error("removed image should be pruned")
	}
	if c.Len() != 1 {
		t.Errorf("Len = %d, want 1", c.Len())
	}
}

// A malformed verbose blob must not poison the whole refresh: the other
// images still get cached and no panic occurs.
func TestRefreshTolerantOfPerImageFailure(t *testing.T) {
	f := &cri.Fake{
		Images:  []cri.Image{{ID: "sha256:good"}, {ID: "sha256:bad"}},
		Created: map[string]time.Time{"sha256:good": time.Unix(1000, 0)},
		// sha256:bad is absent from Created, so ImageCreated errors for it.
	}
	c := New(f, time.Minute, quietLogger())

	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh should not fail on per-image errors: %v", err)
	}
	if _, ok := c.Get("sha256:good"); !ok {
		t.Error("good image should be cached")
	}
	if _, ok := c.Get("sha256:bad"); ok {
		t.Error("bad image should not be cached")
	}
}

func TestRefreshFailsWhenListImagesFails(t *testing.T) {
	f := &cri.Fake{ListImagesErr: errors.New("boom")}
	c := New(f, time.Minute, quietLogger())

	if err := c.Refresh(context.Background()); err == nil {
		t.Fatal("expected error when ListImages fails")
	}
	if !c.LastRefresh().IsZero() {
		t.Error("LastRefresh must not advance on a failed refresh")
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	f := &cri.Fake{}
	c := New(f, time.Millisecond, quietLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}
}
