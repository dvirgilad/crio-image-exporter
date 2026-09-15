package cri

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestFakeReturnsConfiguredImages(t *testing.T) {
	f := &Fake{Images: []Image{{ID: "sha256:aaa", Size: 100}}}
	got, err := f.ListImages(context.Background())
	if err != nil {
		t.Fatalf("ListImages: %v", err)
	}
	if len(got) != 1 || got[0].ID != "sha256:aaa" {
		t.Fatalf("got %+v", got)
	}
}

func TestFakeInjectsPerMethodErrors(t *testing.T) {
	sentinel := errors.New("boom")
	f := &Fake{
		Images:        []Image{{ID: "sha256:aaa"}},
		ListImagesErr: sentinel,
		Filesystems:   []Filesystem{{Mountpoint: "/var/lib/containers"}},
	}
	if _, err := f.ListImages(context.Background()); !errors.Is(err, sentinel) {
		t.Fatalf("ListImages err = %v, want %v", err, sentinel)
	}
	// An error on one method must not affect another.
	if _, err := f.ImageFsInfo(context.Background()); err != nil {
		t.Fatalf("ImageFsInfo: %v", err)
	}
}

func TestFakeCountsImageCreatedCalls(t *testing.T) {
	f := &Fake{Created: map[string]time.Time{"sha256:aaa": time.Unix(1000, 0)}}
	for i := 0; i < 3; i++ {
		if _, err := f.ImageCreated(context.Background(), "sha256:aaa"); err != nil {
			t.Fatalf("ImageCreated: %v", err)
		}
	}
	if f.ImageCreatedCalls("sha256:aaa") != 3 {
		t.Fatalf("calls = %d, want 3", f.ImageCreatedCalls("sha256:aaa"))
	}
}

func TestFakeImageCreatedUnknownImage(t *testing.T) {
	f := &Fake{}
	if _, err := f.ImageCreated(context.Background(), "sha256:missing"); err == nil {
		t.Fatal("expected error for unknown image")
	}
}
