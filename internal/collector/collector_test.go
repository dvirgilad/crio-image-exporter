package collector

import (
	"errors"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/dvirgilad/crio-image-exporter/internal/config"
	"github.com/dvirgilad/crio-image-exporter/internal/cri"
)

func testConfig(t *testing.T, args ...string) *config.Config {
	t.Helper()
	cfg, err := config.Parse(args, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("config.Parse: %v", err)
	}
	return cfg
}

func TestSingleImageSingleTag(t *testing.T) {
	f := &cri.Fake{Images: []cri.Image{{
		ID:          "sha256:aaa",
		RepoTags:    []string{"quay.io/foo/bar:v1"},
		RepoDigests: []string{"quay.io/foo/bar@sha256:ddd"},
		Size:        100,
	}}}
	c := New(f, testConfig(t), Options{})

	want := `
# HELP crio_image_size_bytes Apparent size of the image in bytes, not deduplicated across shared layers.
# TYPE crio_image_size_bytes gauge
crio_image_size_bytes{image_id="sha256:aaa"} 100
# HELP crio_image_info Image name metadata. Join on image_id.
# TYPE crio_image_info gauge
crio_image_info{digest="sha256:ddd",image_id="sha256:aaa",repository="quay.io/foo/bar",tag="v1"} 1
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(want),
		"crio_image_size_bytes", "crio_image_info"); err != nil {
		t.Error(err)
	}
}

// An image with three tags must produce ONE size series and THREE info series.
// Denormalizing tags into the size metric would make sum() triple-count.
func TestMultiTagImageEmitsOneSizeSeries(t *testing.T) {
	f := &cri.Fake{Images: []cri.Image{{
		ID:       "sha256:aaa",
		RepoTags: []string{"quay.io/foo/bar:v1", "quay.io/foo/bar:latest", "registry.local/bar:v1"},
		Size:     100,
	}}}
	c := New(f, testConfig(t), Options{})

	if got := testutil.CollectAndCount(c, "crio_image_size_bytes"); got != 1 {
		t.Errorf("size series = %d, want 1", got)
	}
	if got := testutil.CollectAndCount(c, "crio_image_info"); got != 3 {
		t.Errorf("info series = %d, want 3", got)
	}
}

func TestUntaggedImageStillJoinable(t *testing.T) {
	f := &cri.Fake{Images: []cri.Image{{ID: "sha256:aaa", Size: 100}}}
	c := New(f, testConfig(t), Options{})

	want := `
# HELP crio_image_info Image name metadata. Join on image_id.
# TYPE crio_image_info gauge
crio_image_info{digest="<none>",image_id="sha256:aaa",repository="<none>",tag="<none>"} 1
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(want), "crio_image_info"); err != nil {
		t.Error(err)
	}
}

func TestPinnedImage(t *testing.T) {
	f := &cri.Fake{Images: []cri.Image{
		{ID: "sha256:aaa", Pinned: true},
		{ID: "sha256:bbb", Pinned: false},
	}}
	c := New(f, testConfig(t), Options{})

	want := `
# HELP crio_image_pinned Whether the image is exempt from runtime garbage collection.
# TYPE crio_image_pinned gauge
crio_image_pinned{image_id="sha256:aaa"} 1
crio_image_pinned{image_id="sha256:bbb"} 0
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(want), "crio_image_pinned"); err != nil {
		t.Error(err)
	}
}

func TestNodeAggregates(t *testing.T) {
	f := &cri.Fake{Images: []cri.Image{
		{ID: "sha256:aaa", Size: 100},
		{ID: "sha256:bbb", Size: 250},
	}}
	c := New(f, testConfig(t), Options{})

	want := `
# HELP crio_images_total Number of images present on the node.
# TYPE crio_images_total gauge
crio_images_total 2
# HELP crio_images_apparent_size_bytes_total Sum of apparent image sizes. NOT disk usage; see crio_image_filesystem_used_bytes.
# TYPE crio_images_apparent_size_bytes_total gauge
crio_images_apparent_size_bytes_total 350
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(want),
		"crio_images_total", "crio_images_apparent_size_bytes_total"); err != nil {
		t.Error(err)
	}
}

func TestZeroImagesEmitsZeroedAggregates(t *testing.T) {
	c := New(&cri.Fake{}, testConfig(t), Options{})

	want := `
# HELP crio_images_total Number of images present on the node.
# TYPE crio_images_total gauge
crio_images_total 0
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(want), "crio_images_total"); err != nil {
		t.Error(err)
	}
	if got := testutil.CollectAndCount(c, "crio_image_size_bytes"); got != 0 {
		t.Errorf("size series = %d, want 0", got)
	}
}

func TestContainerCountsPerImage(t *testing.T) {
	f := &cri.Fake{
		Images: []cri.Image{{ID: "sha256:aaa"}, {ID: "sha256:bbb"}},
		Containers: []cri.Container{
			{ID: "c1", ImageRef: "sha256:aaa", State: "running"},
			{ID: "c2", ImageRef: "sha256:aaa", State: "running"},
			{ID: "c3", ImageRef: "sha256:aaa", State: "exited"},
		},
	}
	c := New(f, testConfig(t), Options{})

	want := `
# HELP crio_image_containers Number of containers currently referencing this image.
# TYPE crio_image_containers gauge
crio_image_containers{image_id="sha256:aaa"} 3
crio_image_containers{image_id="sha256:bbb"} 0
# HELP crio_containers_total Number of containers on the node by state.
# TYPE crio_containers_total gauge
crio_containers_total{state="exited"} 1
crio_containers_total{state="running"} 2
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(want),
		"crio_image_containers", "crio_containers_total"); err != nil {
		t.Error(err)
	}
}

func TestFilesystemMetrics(t *testing.T) {
	f := &cri.Fake{
		Images:      []cri.Image{{ID: "sha256:aaa", Size: 300}},
		Filesystems: []cri.Filesystem{{Mountpoint: "/var/lib/containers/storage", UsedBytes: 100, InodesUsed: 42}},
	}
	c := New(f, testConfig(t), Options{})

	want := `
# HELP crio_image_filesystem_used_bytes Real bytes used on the image filesystem.
# TYPE crio_image_filesystem_used_bytes gauge
crio_image_filesystem_used_bytes{filesystem="/var/lib/containers/storage"} 100
# HELP crio_image_filesystem_inodes_used Inodes used on the image filesystem.
# TYPE crio_image_filesystem_inodes_used gauge
crio_image_filesystem_inodes_used{filesystem="/var/lib/containers/storage"} 42
# HELP crio_image_deduplication_ratio Apparent image size total divided by real filesystem bytes used. Values above 1 mean per-image apparent sizes overstate disk.
# TYPE crio_image_deduplication_ratio gauge
crio_image_deduplication_ratio 3
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(want),
		"crio_image_filesystem_used_bytes", "crio_image_filesystem_inodes_used",
		"crio_image_deduplication_ratio"); err != nil {
		t.Error(err)
	}
}

// A zero denominator must not produce +Inf or NaN in the ratio.
func TestDeduplicationRatioAbsentWhenFilesystemUnknown(t *testing.T) {
	f := &cri.Fake{Images: []cri.Image{{ID: "sha256:aaa", Size: 300}}}
	c := New(f, testConfig(t), Options{})

	if got := testutil.CollectAndCount(c, "crio_image_deduplication_ratio"); got != 0 {
		t.Errorf("ratio series = %d, want 0 when no filesystem reported", got)
	}
}

// Each RPC fails independently. The others must still produce metrics.
func TestPartialFailurePerRPC(t *testing.T) {
	boom := errors.New("rpc failed")

	cases := []struct {
		name          string
		mutate        func(*cri.Fake)
		absentMetric  string
		presentMetric string
		errLabel      string
	}{
		{
			name:          "ImageFsInfo fails",
			mutate:        func(f *cri.Fake) { f.ImageFsInfoErr = boom },
			absentMetric:  "crio_image_filesystem_used_bytes",
			presentMetric: "crio_image_size_bytes",
			errLabel:      "ImageFsInfo",
		},
		{
			name:          "ListContainers fails",
			mutate:        func(f *cri.Fake) { f.ListContainersErr = boom },
			absentMetric:  "crio_containers_total",
			presentMetric: "crio_image_size_bytes",
			errLabel:      "ListContainers",
		},
		{
			name:          "Version fails",
			mutate:        func(f *cri.Fake) { f.VersionErr = boom },
			absentMetric:  "crio_runtime_info",
			presentMetric: "crio_image_size_bytes",
			errLabel:      "Version",
		},
		{
			name:          "ListImages fails",
			mutate:        func(f *cri.Fake) { f.ListImagesErr = boom },
			absentMetric:  "crio_image_size_bytes",
			presentMetric: "crio_containers_total",
			errLabel:      "ListImages",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &cri.Fake{
				Images:      []cri.Image{{ID: "sha256:aaa", Size: 100}},
				Filesystems: []cri.Filesystem{{Mountpoint: "/x", UsedBytes: 50}},
				Containers:  []cri.Container{{ID: "c1", ImageRef: "sha256:aaa", State: "running"}},
				Runtime:     cri.RuntimeInfo{Name: "cri-o", Version: "1.30.0"},
			}
			tc.mutate(f)
			c := New(f, testConfig(t), Options{})

			if got := testutil.CollectAndCount(c, tc.absentMetric); got != 0 {
				t.Errorf("%s series = %d, want 0", tc.absentMetric, got)
			}
			if got := testutil.CollectAndCount(c, tc.presentMetric); got == 0 {
				t.Errorf("%s must still be emitted when another RPC fails", tc.presentMetric)
			}
			if got := testutil.ToFloat64(mustGauge(t, c, "crio_image_exporter_scrape_success")); got != 0 {
				t.Errorf("scrape_success = %v, want 0", got)
			}
		})
	}
}

func TestScrapeSuccessOneWhenHealthy(t *testing.T) {
	f := &cri.Fake{
		Images:      []cri.Image{{ID: "sha256:aaa", Size: 100}},
		Filesystems: []cri.Filesystem{{Mountpoint: "/x", UsedBytes: 50}},
		Runtime:     cri.RuntimeInfo{Name: "cri-o", Version: "1.30.0"},
	}
	c := New(f, testConfig(t), Options{})

	want := `
# HELP crio_image_exporter_scrape_success Whether every CRI call in the last scrape succeeded.
# TYPE crio_image_exporter_scrape_success gauge
crio_image_exporter_scrape_success 1
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(want),
		"crio_image_exporter_scrape_success"); err != nil {
		t.Error(err)
	}
}

func TestBuildAndRuntimeInfo(t *testing.T) {
	f := &cri.Fake{Runtime: cri.RuntimeInfo{Name: "cri-o", Version: "1.30.0", APIVersion: "v1"}}
	c := New(f, testConfig(t), Options{Version: "1.2.3", Revision: "abc1234"})

	want := `
# HELP crio_runtime_info Container runtime identification.
# TYPE crio_runtime_info gauge
crio_runtime_info{api_version="v1",runtime_name="cri-o",runtime_version="1.30.0"} 1
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(want), "crio_runtime_info"); err != nil {
		t.Error(err)
	}
	if got := testutil.CollectAndCount(c, "crio_image_exporter_build_info"); got != 1 {
		t.Errorf("build_info series = %d, want 1", got)
	}
}

// mustGauge extracts a single-series metric from c as a standalone collector.
func mustGauge(t *testing.T, c *Collector, name string) prometheus.Collector {
	t.Helper()
	g := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: name + "_extracted"}, nil)
	mfs, err := prometheus.Gatherers{newRegistryWith(t, c)}.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() == name {
			g.WithLabelValues().Set(mf.GetMetric()[0].GetGauge().GetValue())
			return g
		}
	}
	t.Fatalf("metric %s not found", name)
	return nil
}

func newRegistryWith(t *testing.T, c prometheus.Collector) *prometheus.Registry {
	t.Helper()
	r := prometheus.NewRegistry()
	if err := r.Register(c); err != nil {
		t.Fatalf("register: %v", err)
	}
	return r
}
