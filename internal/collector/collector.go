// Package collector turns CRI-O state into Prometheus metrics.
//
// It imports no CRI protobuf types: everything arrives through the cri.Client
// interface, so every test here runs without a container runtime.
package collector

import (
	"context"
	"runtime"
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
	imageSize       *prometheus.Desc
	imageInfo       *prometheus.Desc
	imagePinned     *prometheus.Desc
	imagesTotal     *prometheus.Desc
	apparentTotal   *prometheus.Desc
	imageContainers *prometheus.Desc
	containersTotal *prometheus.Desc
	fsUsedBytes     *prometheus.Desc
	fsInodesUsed    *prometheus.Desc
	dedupRatio      *prometheus.Desc
	runtimeInfo     *prometheus.Desc
	buildInfo       *prometheus.Desc
	scrapeDuration  *prometheus.Desc
	scrapeSuccess   *prometheus.Desc
	criErrorsTotal  *prometheus.Desc
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
			imageContainers: prometheus.NewDesc(
				"crio_image_containers",
				"Number of containers currently referencing this image. Absent if container listing failed.",
				[]string{"image_id"}, nil),
			containersTotal: prometheus.NewDesc(
				"crio_containers_total",
				"Number of containers on the node by state.",
				[]string{"state"}, nil),
			fsUsedBytes: prometheus.NewDesc(
				"crio_image_filesystem_used_bytes",
				"Real bytes used on the image filesystem.",
				[]string{"filesystem"}, nil),
			fsInodesUsed: prometheus.NewDesc(
				"crio_image_filesystem_inodes_used",
				"Inodes used on the image filesystem.",
				[]string{"filesystem"}, nil),
			dedupRatio: prometheus.NewDesc(
				"crio_image_deduplication_ratio",
				"Apparent image size total divided by real filesystem bytes used. Values above 1 mean per-image apparent sizes overstate disk.",
				nil, nil),
			runtimeInfo: prometheus.NewDesc(
				"crio_runtime_info",
				"Container runtime identification.",
				[]string{"runtime_name", "runtime_version", "api_version"}, nil),
			buildInfo: prometheus.NewDesc(
				"crio_image_exporter_build_info",
				"Exporter build identification.",
				[]string{"version", "revision", "go_version"}, nil),
			scrapeDuration: prometheus.NewDesc(
				"crio_image_exporter_scrape_duration_seconds",
				"Duration of the last scrape.",
				nil, nil),
			scrapeSuccess: prometheus.NewDesc(
				"crio_image_exporter_scrape_success",
				"Whether every CRI call in the last scrape succeeded.",
				nil, nil),
			criErrorsTotal: prometheus.NewDesc(
				"crio_image_exporter_cri_errors_total",
				"Total CRI RPC failures by RPC name.",
				[]string{"rpc"}, nil),
		},
	}
}

func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.descs.imageSize
	ch <- c.descs.imageInfo
	ch <- c.descs.imagePinned
	ch <- c.descs.imagesTotal
	ch <- c.descs.apparentTotal
	ch <- c.descs.imageContainers
	ch <- c.descs.containersTotal
	ch <- c.descs.fsUsedBytes
	ch <- c.descs.fsInodesUsed
	ch <- c.descs.dedupRatio
	ch <- c.descs.runtimeInfo
	ch <- c.descs.buildInfo
	ch <- c.descs.scrapeDuration
	ch <- c.descs.scrapeSuccess
	ch <- c.descs.criErrorsTotal
}

func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	ctx := context.Background()
	start := time.Now()

	// Each section reports its own success. A failure in one must not prevent
	// the others from emitting; that is why this is not a chain of early
	// returns over a shared error.
	containersByImage, containersOK := c.collectContainers(ctx, ch)
	apparentTotal, imagesOK := c.collectImages(ctx, ch, containersByImage, containersOK)
	usedBytes, fsOK := c.collectFilesystems(ctx, ch)
	versionOK := c.collectRuntimeInfo(ctx, ch)

	// The ratio needs both halves and a non-zero denominator. Emitting +Inf
	// would poison dashboards, so an unknown ratio is simply absent.
	if imagesOK && fsOK && usedBytes > 0 {
		ch <- prometheus.MustNewConstMetric(
			c.descs.dedupRatio, prometheus.GaugeValue,
			float64(apparentTotal)/float64(usedBytes))
	}

	c.collectStorage(ch)
	c.collectAgeCacheHealth(ch)

	success := containersOK && imagesOK && fsOK && versionOK
	ch <- prometheus.MustNewConstMetric(c.descs.scrapeSuccess, prometheus.GaugeValue, boolValue(success))
	ch <- prometheus.MustNewConstMetric(c.descs.scrapeDuration, prometheus.GaugeValue, time.Since(start).Seconds())
	ch <- prometheus.MustNewConstMetric(
		c.descs.buildInfo, prometheus.GaugeValue, 1,
		c.opts.Version, c.opts.Revision, runtime.Version())

	c.mu.Lock()
	for rpc, n := range c.criErrors {
		ch <- prometheus.MustNewConstMetric(c.descs.criErrorsTotal, prometheus.CounterValue, n, rpc)
	}
	c.mu.Unlock()
}

// recordError increments the persistent per-RPC error counter.
func (c *Collector) recordError(rpc string) {
	c.mu.Lock()
	c.criErrors[rpc]++
	c.mu.Unlock()
}

// collectContainers returns a count of containers per image ID.
func (c *Collector) collectContainers(ctx context.Context, ch chan<- prometheus.Metric) (map[string]int, bool) {
	containers, err := c.client.ListContainers(ctx)
	if err != nil {
		c.recordError("ListContainers")
		return nil, false
	}
	byImage := make(map[string]int, len(containers))
	byState := map[string]int{}
	for _, ctr := range containers {
		byImage[ctr.ImageRef]++
		byState[ctr.State]++
	}
	for state, n := range byState {
		ch <- prometheus.MustNewConstMetric(
			c.descs.containersTotal, prometheus.GaugeValue, float64(n), state)
	}
	return byImage, true
}

func (c *Collector) collectImages(ctx context.Context, ch chan<- prometheus.Metric, containersByImage map[string]int, containersOK bool) (uint64, bool) {
	images, err := c.client.ListImages(ctx)
	if err != nil {
		c.recordError("ListImages")
		return 0, false
	}

	var apparentTotal uint64
	for _, img := range images {
		apparentTotal += img.Size
	}
	ch <- prometheus.MustNewConstMetric(c.descs.imagesTotal, prometheus.GaugeValue, float64(len(images)))
	ch <- prometheus.MustNewConstMetric(c.descs.apparentTotal, prometheus.GaugeValue, float64(apparentTotal))

	for _, img := range images {
		ch <- prometheus.MustNewConstMetric(c.descs.imageSize, prometheus.GaugeValue, float64(img.Size), img.ID)
		ch <- prometheus.MustNewConstMetric(c.descs.imagePinned, prometheus.GaugeValue, boolValue(img.Pinned), img.ID)
		// Only emit per-image container counts if container listing succeeded.
		// If listing failed, the count is unknown rather than zero.
		if containersOK {
			ch <- prometheus.MustNewConstMetric(
				c.descs.imageContainers, prometheus.GaugeValue,
				float64(containersByImage[img.ID]), img.ID)
		}

		for _, labels := range infoLabels(img) {
			ch <- prometheus.MustNewConstMetric(
				c.descs.imageInfo, prometheus.GaugeValue, 1,
				labels.imageID, labels.repository, labels.tag, labels.digest)
		}
	}
	return apparentTotal, true
}

func (c *Collector) collectFilesystems(ctx context.Context, ch chan<- prometheus.Metric) (uint64, bool) {
	filesystems, err := c.client.ImageFsInfo(ctx)
	if err != nil {
		c.recordError("ImageFsInfo")
		return 0, false
	}
	var total uint64
	for _, fs := range filesystems {
		ch <- prometheus.MustNewConstMetric(
			c.descs.fsUsedBytes, prometheus.GaugeValue, float64(fs.UsedBytes), fs.Mountpoint)
		ch <- prometheus.MustNewConstMetric(
			c.descs.fsInodesUsed, prometheus.GaugeValue, float64(fs.InodesUsed), fs.Mountpoint)
		total += fs.UsedBytes
	}
	return total, true
}

func (c *Collector) collectRuntimeInfo(ctx context.Context, ch chan<- prometheus.Metric) bool {
	info, err := c.client.Version(ctx)
	if err != nil {
		c.recordError("Version")
		return false
	}
	ch <- prometheus.MustNewConstMetric(
		c.descs.runtimeInfo, prometheus.GaugeValue, 1,
		info.Name, info.Version, info.APIVersion)
	return true
}

// collectStorage and collectAgeCacheHealth are filled in by Tasks 7 and 9.
func (c *Collector) collectStorage(chan<- prometheus.Metric)        {}
func (c *Collector) collectAgeCacheHealth(chan<- prometheus.Metric) {}

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
