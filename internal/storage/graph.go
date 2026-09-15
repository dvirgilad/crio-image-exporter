// Package storage derives exact per-image byte attribution from
// containers/storage metadata.
//
// CRI reports apparent image sizes that double-count shared layers, and it
// exposes no per-layer sizes, so exact attribution is impossible from CRI
// alone. This package reads the on-disk layer graph instead.
//
// That on-disk format is an implementation detail of containers/storage, not
// an API contract. Everything here is therefore opt-in, and every failure
// degrades to absent metrics rather than a broken exporter.
package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dvirgilad/crio-image-exporter/internal/collector"
)

// layer mirrors the fields this package needs from a containers/storage layer.
type layer struct {
	ID     string `json:"id"`
	Parent string `json:"parent"`
	// DiffSize is the uncompressed size of this layer alone. It can be absent
	// or negative when the layer was written without size accounting.
	DiffSize int64 `json:"diff-size"`
}

// image mirrors the fields this package needs from a containers/storage image.
// TopLayer is serialized as "layer", not "top-layer".
type image struct {
	ID       string `json:"id"`
	TopLayer string `json:"layer"`
}

// Parse reads the layer graph under root and computes per-image attribution.
func Parse(root string) (*collector.Attribution, error) {
	layers, err := readLayers(root)
	if err != nil {
		return nil, err
	}
	images, err := readImages(root)
	if err != nil {
		return nil, err
	}
	return attribute(layers, images), nil
}

// readLayers loads every layer record. The metadata directory is named after
// the active storage driver ("overlay-layers" in practice), so it is globbed
// rather than hardcoded. Newer containers/storage also writes
// volatile-layers.json; both are merged when present.
func readLayers(root string) (map[string]layer, error) {
	paths, err := filepath.Glob(filepath.Join(root, "*-layers", "layers.json"))
	if err != nil {
		return nil, fmt.Errorf("glob layers.json: %w", err)
	}
	volatile, err := filepath.Glob(filepath.Join(root, "*-layers", "volatile-layers.json"))
	if err != nil {
		return nil, fmt.Errorf("glob volatile-layers.json: %w", err)
	}
	paths = append(paths, volatile...)
	if len(paths) == 0 {
		return nil, fmt.Errorf("no layers.json found under %s", root)
	}

	out := map[string]layer{}
	for _, p := range paths {
		var batch []layer
		if err := readJSON(p, &batch); err != nil {
			return nil, err
		}
		for _, l := range batch {
			out[l.ID] = l
		}
	}
	return out, nil
}

func readImages(root string) ([]image, error) {
	paths, err := filepath.Glob(filepath.Join(root, "*-images", "images.json"))
	if err != nil {
		return nil, fmt.Errorf("glob images.json: %w", err)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no images.json found under %s", root)
	}

	var out []image
	for _, p := range paths {
		var batch []image
		if err := readJSON(p, &batch); err != nil {
			return nil, err
		}
		out = append(out, batch...)
	}
	return out, nil
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

// attribute walks each image's parent chain to find its layer set, counts how
// many images reference each layer, then splits every image's bytes into
// exclusive (refcount 1) and shared (refcount > 1).
func attribute(layers map[string]layer, images []image) *collector.Attribution {
	chains := make(map[string][]string, len(images))
	refcount := make(map[string]int, len(layers))

	for _, img := range images {
		chain := layerChain(layers, img.TopLayer)
		chains[img.ID] = chain
		for _, id := range chain {
			refcount[id]++
		}
	}

	att := &collector.Attribution{
		Exclusive:   make(map[string]uint64, len(images)),
		Shared:      make(map[string]uint64, len(images)),
		LayerCount:  len(layers),
		RefreshedAt: time.Now(),
	}

	for imageID, chain := range chains {
		var exclusive, shared uint64
		for _, layerID := range chain {
			size := layerSize(layers[layerID])
			if refcount[layerID] > 1 {
				shared += size
			} else {
				exclusive += size
			}
		}
		att.Exclusive[normalizeID(imageID)] = exclusive
		att.Shared[normalizeID(imageID)] = shared
	}

	// Every distinct layer counts once, including layers no image references.
	// Those orphans are real bytes on disk, usually from interrupted pulls.
	for _, l := range layers {
		att.TotalDeduplicated += layerSize(l)
	}
	return att
}

// layerChain returns the layer and all its ancestors. A missing parent record
// ends the walk; a cycle would too, guarded by the visited set.
func layerChain(layers map[string]layer, top string) []string {
	var chain []string
	visited := map[string]struct{}{}
	for id := top; id != ""; {
		if _, seen := visited[id]; seen {
			break
		}
		l, ok := layers[id]
		if !ok {
			break
		}
		visited[id] = struct{}{}
		chain = append(chain, id)
		id = l.Parent
	}
	return chain
}

// layerSize treats unknown or negative sizes as zero rather than guessing.
func layerSize(l layer) uint64 {
	if l.DiffSize <= 0 {
		return 0
	}
	return uint64(l.DiffSize)
}

// normalizeID strips the algorithm prefix so storage IDs (bare hex) match CRI
// image IDs (usually "sha256:"-prefixed). Without this nothing ever joins.
func normalizeID(id string) string {
	if i := strings.Index(id, ":"); i >= 0 {
		return id[i+1:]
	}
	return id
}

// Graph keeps a periodically refreshed snapshot of the attribution.
type Graph struct {
	root     string
	interval time.Duration
	log      *slog.Logger

	mu       sync.RWMutex
	snapshot *collector.Attribution
	errors   map[string]float64
}

func New(root string, interval time.Duration, log *slog.Logger) *Graph {
	return &Graph{
		root:     root,
		interval: interval,
		log:      log,
		errors:   map[string]float64{},
	}
}

func (g *Graph) Run(ctx context.Context) {
	if err := g.Refresh(); err != nil {
		g.log.Warn("initial storage graph refresh failed", "error", err)
	}
	ticker := time.NewTicker(g.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := g.Refresh(); err != nil {
				g.log.Warn("storage graph refresh failed", "error", err)
			}
		}
	}
}

// Refresh reparses the graph. On failure the previous snapshot is retained:
// CRI-O writes these files concurrently, so a torn read is expected and must
// not punch holes in the metrics.
func (g *Graph) Refresh() error {
	att, err := Parse(g.root)
	if err != nil {
		g.mu.Lock()
		g.errors["parse"]++
		g.mu.Unlock()
		return err
	}
	g.mu.Lock()
	g.snapshot = att
	g.mu.Unlock()
	return nil
}

// Snapshot returns the most recent successful attribution, or nil if none has
// succeeded yet. The returned value is never mutated after publication.
func (g *Graph) Snapshot() *collector.Attribution {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.snapshot
}

func (g *Graph) Errors() map[string]float64 {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make(map[string]float64, len(g.errors))
	for k, v := range g.errors {
		out[k] = v
	}
	return out
}

var _ collector.StorageSource = (*Graph)(nil)
