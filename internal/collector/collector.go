// Package collector turns CRI-O state into Prometheus metrics.
//
// It imports no CRI protobuf types: everything arrives through the cri.Client
// interface, so every test here runs without a container runtime.
package collector

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/dvirgilad/crio-image-exporter/internal/config"
	"github.com/dvirgilad/crio-image-exporter/internal/cri"
)

// none is the label value used when an image has no repo tag or digest. Using
// a sentinel rather than an empty string keeps the series joinable and visible
// in queries, which is exactly when an operator most wants to see it.
const none = "<none>"

// AgeSource supplies image creation times. Implemented by internal/agecache in
// Task 7. Nil when --collect-image-age is false.
type AgeSource interface {
	Get(imageID string) (time.Time, bool)
	Len() int
	LastRefresh() time.Time
}

// StorageSource supplies exact layer attribution. Implemented by
// internal/storage in Task 8. Nil when --storage-root is empty.
type StorageSource interface {
	Snapshot() *Attribution
	// Errors counts parse failures by reason, so a silently degrading
	// storage graph is visible rather than just showing up as absent metrics.
	Errors() map[string]float64
}

// Attribution is the result of splitting image bytes by layer sharing.
// Declared here so collector does not import internal/storage, keeping the
// dependency pointing one way.
type Attribution struct {
	// Exclusive maps image ID to bytes reclaimed if that image is deleted.
	Exclusive map[string]uint64
	// Shared maps image ID to bytes in layers it shares with other images.
	Shared map[string]uint64
	// TotalDeduplicated is the sum of distinct layer sizes on the node.
	TotalDeduplicated uint64
	// LayerCount is the number of distinct layers.
	LayerCount int
	// RefreshedAt is when this snapshot was produced.
	RefreshedAt time.Time
}

// Options carries dependencies that are not configuration.
type Options struct {
	Version  string
	Revision string
	// Age is nil when image age collection is disabled.
	Age AgeSource
	// Storage is nil when storage inspection is disabled.
	Storage StorageSource
}

type Collector struct {
	client cri.Client
	cfg    *config.Config
	opts   Options

	// criErrors counts RPC failures across scrapes, so it must persist rather
	// than be rebuilt per Collect.
	mu        sync.Mutex
	criErrors map[string]float64

	descs descriptors
}

type descriptors struct {
	imageSize     *prometheus.Desc
	imageInfo     *prometheus.Desc
	imagePinned   *prometheus.Desc
	imagesTotal   *prometheus.Desc
	apparentTotal *prometheus.Desc
}

func New(client cri.Client, cfg *config.Config, opts Options) *Collector {
	return &Collector{
		client:    client,
		cfg:       cfg,
		opts:      opts,
		criErrors: map[string]float64{},
		descs: descriptors{
			imageSize: prometheus.NewDesc(
				"crio_image_size_bytes",
				"Apparent size of the image in bytes, not deduplicated across shared layers.",
				[]string{"image_id"}, nil),
			imageInfo: prometheus.NewDesc(
				"crio_image_info",
				"Image name metadata. Join on image_id.",
				[]string{"image_id", "repository", "tag", "digest"}, nil),
			imagePinned: prometheus.NewDesc(
				"crio_image_pinned",
				"Whether the image is exempt from runtime garbage collection.",
				[]string{"image_id"}, nil),
			imagesTotal: prometheus.NewDesc(
				"crio_images_total",
				"Number of images present on the node.",
				nil, nil),
			apparentTotal: prometheus.NewDesc(
				"crio_images_apparent_size_bytes_total",
				"Sum of apparent image sizes. NOT disk usage; see crio_image_filesystem_used_bytes.",
				nil, nil),
		},
	}
}

func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.descs.imageSize
	ch <- c.descs.imageInfo
	ch <- c.descs.imagePinned
	ch <- c.descs.imagesTotal
	ch <- c.descs.apparentTotal
}

func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	ctx := context.Background()
	c.collectImages(ctx, ch)
}

// recordError increments the persistent per-RPC error counter.
func (c *Collector) recordError(rpc string) {
	c.mu.Lock()
	c.criErrors[rpc]++
	c.mu.Unlock()
}

func (c *Collector) collectImages(ctx context.Context, ch chan<- prometheus.Metric) {
	images, err := c.client.ListImages(ctx)
	if err != nil {
		c.recordError("ListImages")
		return
	}

	var apparentTotal uint64
	for _, img := range images {
		apparentTotal += img.Size
	}
	ch <- prometheus.MustNewConstMetric(c.descs.imagesTotal, prometheus.GaugeValue, float64(len(images)))
	ch <- prometheus.MustNewConstMetric(c.descs.apparentTotal, prometheus.GaugeValue, float64(apparentTotal))

	for _, img := range images {
		ch <- prometheus.MustNewConstMetric(
			c.descs.imageSize, prometheus.GaugeValue, float64(img.Size), img.ID)
		ch <- prometheus.MustNewConstMetric(
			c.descs.imagePinned, prometheus.GaugeValue, boolValue(img.Pinned), img.ID)

		for _, labels := range infoLabels(img) {
			ch <- prometheus.MustNewConstMetric(
				c.descs.imageInfo, prometheus.GaugeValue, 1,
				labels.imageID, labels.repository, labels.tag, labels.digest)
		}
	}
}

type infoLabelSet struct {
	imageID, repository, tag, digest string
}

// infoLabels produces one label set per repo tag. An untagged image still
// produces a single set with sentinel values so it remains joinable.
func infoLabels(img cri.Image) []infoLabelSet {
	digest := none
	if len(img.RepoDigests) > 0 {
		digest = digestOf(img.RepoDigests[0])
	}
	if len(img.RepoTags) == 0 {
		return []infoLabelSet{{imageID: img.ID, repository: none, tag: none, digest: digest}}
	}
	out := make([]infoLabelSet, 0, len(img.RepoTags))
	for _, rt := range img.RepoTags {
		repo, tag := splitRepoTag(rt)
		out = append(out, infoLabelSet{imageID: img.ID, repository: repo, tag: tag, digest: digest})
	}
	return out
}

// splitRepoTag splits "quay.io/foo/bar:v1" into repository and tag. A registry
// host may carry a port ("localhost:5000/bar:v1"), so the tag separator is the
// last colon after the last slash.
func splitRepoTag(repoTag string) (repository, tag string) {
	slash := strings.LastIndex(repoTag, "/")
	colon := strings.LastIndex(repoTag, ":")
	if colon < 0 || colon < slash {
		return repoTag, none
	}
	return repoTag[:colon], repoTag[colon+1:]
}

// digestOf extracts the digest from "repo@sha256:abc", returning the hex part.
func digestOf(repoDigest string) string {
	if at := strings.LastIndex(repoDigest, "@"); at >= 0 {
		return repoDigest[at+1:]
	}
	return repoDigest
}

func boolValue(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
