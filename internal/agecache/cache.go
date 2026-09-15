// Package agecache maintains image creation timestamps in the background.
//
// CRI's ListImages carries no creation time; obtaining one costs an
// ImageStatus(verbose) call per image plus parsing a runtime-specific blob.
// Doing that on every scrape would be wasteful, so it happens on an interval.
package agecache

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/dvirgilad/crio-image-exporter/internal/cri"
)

type Cache struct {
	client   cri.Client
	interval time.Duration
	log      *slog.Logger

	mu          sync.RWMutex
	created     map[string]time.Time
	lastRefresh time.Time
}

func New(client cri.Client, interval time.Duration, log *slog.Logger) *Cache {
	return &Cache{
		client:   client,
		interval: interval,
		log:      log,
		created:  map[string]time.Time{},
	}
}

// Run refreshes immediately, then on the configured interval, until ctx is
// cancelled.
func (c *Cache) Run(ctx context.Context) {
	if err := c.Refresh(ctx); err != nil {
		c.log.Warn("initial image age refresh failed", "error", err)
	}
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.Refresh(ctx); err != nil {
				c.log.Warn("image age refresh failed", "error", err)
			}
		}
	}
}

// Refresh queries creation times for images not yet cached and prunes images
// that are gone. Creation time is immutable for a given image ID, so a cached
// entry is never re-queried — this is what keeps steady-state cost near zero.
func (c *Cache) Refresh(ctx context.Context) error {
	images, err := c.client.ListImages(ctx)
	if err != nil {
		return fmt.Errorf("list images: %w", err)
	}

	present := make(map[string]struct{}, len(images))
	var missing []string

	c.mu.RLock()
	for _, img := range images {
		present[img.ID] = struct{}{}
		if _, ok := c.created[img.ID]; !ok {
			missing = append(missing, img.ID)
		}
	}
	c.mu.RUnlock()

	// Query outside the lock: each call is a round trip to CRI-O.
	fetched := make(map[string]time.Time, len(missing))
	for _, id := range missing {
		ts, err := c.client.ImageCreated(ctx, id)
		if err != nil {
			// Per-image failure is expected and tolerable — the verbose blob
			// is not a stable contract. The metric is simply absent.
			c.log.Debug("image creation time unavailable", "image_id", id, "error", err)
			continue
		}
		fetched[id] = ts
	}

	c.mu.Lock()
	for id, ts := range fetched {
		c.created[id] = ts
	}
	for id := range c.created {
		if _, ok := present[id]; !ok {
			delete(c.created, id)
		}
	}
	c.lastRefresh = time.Now()
	c.mu.Unlock()
	return nil
}

func (c *Cache) Get(imageID string) (time.Time, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	ts, ok := c.created[imageID]
	return ts, ok
}

func (c *Cache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.created)
}

func (c *Cache) LastRefresh() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastRefresh
}
