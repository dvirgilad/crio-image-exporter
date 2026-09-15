package storage

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestParseAttributesSharedAndExclusiveBytes(t *testing.T) {
	att, err := Parse("testdata")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if got, want := att.Exclusive["aaaaaaaa"], uint64(50); got != want {
		t.Errorf("exclusive[aaaaaaaa] = %d, want %d", got, want)
	}
	if got, want := att.Shared["aaaaaaaa"], uint64(1000); got != want {
		t.Errorf("shared[aaaaaaaa] = %d, want %d", got, want)
	}
	if got, want := att.Exclusive["bbbbbbbb"], uint64(100); got != want {
		t.Errorf("exclusive[bbbbbbbb] = %d, want %d", got, want)
	}
	if got, want := att.Shared["bbbbbbbb"], uint64(1000); got != want {
		t.Errorf("shared[bbbbbbbb] = %d, want %d", got, want)
	}
}

// The deduplicated total counts every distinct layer once, including layers no
// image references. A gap against ImageFsInfo is a real signal: orphaned
// layers from interrupted pulls.
func TestParseDeduplicatedTotalIncludesOrphans(t *testing.T) {
	att, err := Parse("testdata")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got, want := att.TotalDeduplicated, uint64(2149); got != want {
		t.Errorf("TotalDeduplicated = %d, want %d", got, want)
	}
	if got, want := att.LayerCount, 5; got != want {
		t.Errorf("LayerCount = %d, want %d", got, want)
	}
}

func TestParseMissingRootIsError(t *testing.T) {
	if _, err := Parse(filepath.Join(t.TempDir(), "nonexistent")); err == nil {
		t.Fatal("expected error for missing storage root")
	}
}

func TestParseMalformedJSONIsError(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "overlay-layers", "layers.json"), "{not json")
	mustWrite(t, filepath.Join(root, "overlay-images", "images.json"), "[]")

	if _, err := Parse(root); err == nil {
		t.Fatal("expected error for malformed layers.json")
	}
}

// A failed refresh must retain the previous snapshot rather than blank the
// metrics. CRI-O writes these files concurrently, so torn reads are expected.
func TestRefreshRetainsPreviousSnapshotOnError(t *testing.T) {
	root := t.TempDir()
	mustCopyTree(t, "testdata", root)

	g := New(root, time.Minute, quietLogger())
	if err := g.Refresh(); err != nil {
		t.Fatalf("first Refresh: %v", err)
	}
	before := g.Snapshot()
	if before == nil || before.TotalDeduplicated == 0 {
		t.Fatal("first refresh should produce a snapshot")
	}

	mustWrite(t, filepath.Join(root, "overlay-layers", "layers.json"), "{truncated")
	if err := g.Refresh(); err == nil {
		t.Fatal("expected refresh error on malformed input")
	}

	after := g.Snapshot()
	if after == nil {
		t.Fatal("snapshot must be retained after a failed refresh")
	}
	if after.TotalDeduplicated != before.TotalDeduplicated {
		t.Errorf("snapshot changed after failed refresh: %d -> %d",
			before.TotalDeduplicated, after.TotalDeduplicated)
	}
	if g.Errors()["parse"] != 1 {
		t.Errorf("parse error count = %v, want 1", g.Errors()["parse"])
	}
}

func TestSnapshotNilBeforeFirstRefresh(t *testing.T) {
	if g := New("testdata", time.Minute, quietLogger()); g.Snapshot() != nil {
		t.Error("snapshot should be nil before the first refresh")
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustCopyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}
