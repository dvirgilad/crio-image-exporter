package collector

import (
	"strings"
	"testing"

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
