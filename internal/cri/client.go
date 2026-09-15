package cri

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
)

// GRPCClient talks to CRI-O over its unix socket.
type GRPCClient struct {
	conn    *grpc.ClientConn
	images  runtimeapi.ImageServiceClient
	runtime runtimeapi.RuntimeServiceClient
	timeout time.Duration
}

// Dial connects to a CRI endpoint. The endpoint must carry a unix:// scheme.
// The connection is lazy: failure to reach the socket surfaces on first RPC,
// which is what we want — the exporter must start and report unhealthy rather
// than crash-loop when CRI-O is briefly unavailable.
func Dial(_ context.Context, endpoint string, timeout time.Duration) (*GRPCClient, error) {
	if !strings.HasPrefix(endpoint, "unix://") {
		return nil, fmt.Errorf("cri endpoint must use unix:// scheme, got %q", endpoint)
	}
	conn, err := grpc.NewClient(endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", endpoint, err)
	}
	return &GRPCClient{
		conn:    conn,
		images:  runtimeapi.NewImageServiceClient(conn),
		runtime: runtimeapi.NewRuntimeServiceClient(conn),
		timeout: timeout,
	}, nil
}

func (c *GRPCClient) ListImages(ctx context.Context) ([]Image, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	resp, err := c.images.ListImages(ctx, &runtimeapi.ListImagesRequest{})
	if err != nil {
		return nil, fmt.Errorf("ListImages: %w", err)
	}
	out := make([]Image, 0, len(resp.GetImages()))
	for _, img := range resp.GetImages() {
		out = append(out, Image{
			ID:          img.GetId(),
			RepoTags:    img.GetRepoTags(),
			RepoDigests: img.GetRepoDigests(),
			Size:        img.GetSize(),
			Pinned:      img.GetPinned(),
		})
	}
	return out, nil
}

func (c *GRPCClient) ImageFsInfo(ctx context.Context) ([]Filesystem, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	resp, err := c.images.ImageFsInfo(ctx, &runtimeapi.ImageFsInfoRequest{})
	if err != nil {
		return nil, fmt.Errorf("ImageFsInfo: %w", err)
	}
	out := make([]Filesystem, 0, len(resp.GetImageFilesystems()))
	for _, fs := range resp.GetImageFilesystems() {
		out = append(out, Filesystem{
			Mountpoint: fs.GetFsId().GetMountpoint(),
			UsedBytes:  fs.GetUsedBytes().GetValue(),
			InodesUsed: fs.GetInodesUsed().GetValue(),
		})
	}
	return out, nil
}

func (c *GRPCClient) ListContainers(ctx context.Context) ([]Container, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	resp, err := c.runtime.ListContainers(ctx, &runtimeapi.ListContainersRequest{})
	if err != nil {
		return nil, fmt.Errorf("ListContainers: %w", err)
	}
	out := make([]Container, 0, len(resp.GetContainers()))
	for _, ctr := range resp.GetContainers() {
		out = append(out, Container{
			ID:       ctr.GetId(),
			ImageRef: ctr.GetImageRef(),
			State:    containerState(ctr.GetState()),
		})
	}
	return out, nil
}

func containerState(s runtimeapi.ContainerState) string {
	switch s {
	case runtimeapi.ContainerState_CONTAINER_CREATED:
		return "created"
	case runtimeapi.ContainerState_CONTAINER_RUNNING:
		return "running"
	case runtimeapi.ContainerState_CONTAINER_EXITED:
		return "exited"
	default:
		return "unknown"
	}
}

func (c *GRPCClient) Version(ctx context.Context) (RuntimeInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	resp, err := c.runtime.Version(ctx, &runtimeapi.VersionRequest{})
	if err != nil {
		return RuntimeInfo{}, fmt.Errorf("Version: %w", err)
	}
	return RuntimeInfo{
		Name:       resp.GetRuntimeName(),
		Version:    resp.GetRuntimeVersion(),
		APIVersion: resp.GetRuntimeApiVersion(),
	}, nil
}

// verboseInfo is the shape of the "info" value CRI-O returns from
// ImageStatus(verbose=true). Only the field we need is declared; the blob is
// runtime-specific and not part of the CRI contract, so this may break across
// CRI-O versions. Callers must tolerate an error here.
type verboseInfo struct {
	ImageSpec struct {
		Created string `json:"created"`
	} `json:"imageSpec"`
}

func (c *GRPCClient) ImageCreated(ctx context.Context, imageID string) (time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	resp, err := c.images.ImageStatus(ctx, &runtimeapi.ImageStatusRequest{
		Image:   &runtimeapi.ImageSpec{Image: imageID},
		Verbose: true,
	})
	if err != nil {
		return time.Time{}, fmt.Errorf("ImageStatus %s: %w", imageID, err)
	}
	raw, ok := resp.GetInfo()["info"]
	if !ok {
		return time.Time{}, fmt.Errorf("image %s: verbose response has no %q key", imageID, "info")
	}
	var info verboseInfo
	if err := json.Unmarshal([]byte(raw), &info); err != nil {
		return time.Time{}, fmt.Errorf("image %s: parse verbose info: %w", imageID, err)
	}
	if info.ImageSpec.Created == "" {
		return time.Time{}, fmt.Errorf("image %s: no creation timestamp in verbose info", imageID)
	}
	ts, err := time.Parse(time.RFC3339Nano, info.ImageSpec.Created)
	if err != nil {
		return time.Time{}, fmt.Errorf("image %s: parse created %q: %w", imageID, info.ImageSpec.Created, err)
	}
	return ts, nil
}

func (c *GRPCClient) Close() error { return c.conn.Close() }

var _ Client = (*GRPCClient)(nil)
