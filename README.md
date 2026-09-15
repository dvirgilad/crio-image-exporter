# crio-image-exporter

A Prometheus exporter for per-node container image disk usage on CRI-O.

## 1. What it does

`crio-image-exporter` runs as a DaemonSet, one pod per node, and talks to the
local CRI-O runtime over its Unix socket to report per-image and per-node
disk usage as Prometheus metrics — how many images are on a node, how big
each one is, how much of the image filesystem is really in use, and,
optionally, exactly how many bytes each image would free up if deleted. In
one line: **per-node attribution of container image disk usage on CRI-O.**

## 2. Quick start

Against an OpenShift cluster with the Prometheus Operator installed:

```bash
helm install crio-image-exporter ./charts/crio-image-exporter \
  -n prometheus-system --create-namespace

oc rollout status daemonset/crio-image-exporter -n prometheus-system

oc get servicemonitor -n prometheus-system crio-image-exporter
```

For a non-OpenShift Kubernetes cluster, disable the OpenShift-specific
pieces (kube-rbac-proxy and TLS) and serve metrics in plaintext:

```bash
helm install crio-image-exporter ./charts/crio-image-exporter \
  --set kubeRBACProxy.enabled=false
```

See [`charts/crio-image-exporter/README.md`](charts/crio-image-exporter/README.md)
for the full values reference.

## 3. The image size caveat

**Read this before you alert on anything.** `crio_image_size_bytes` is an
*apparent* size. It does not account for layers shared between images: ten
images built on the same base each report that base's bytes. Summing
apparent sizes therefore overstates disk, often by 3x or more.

- To alert on disk, use `crio_image_filesystem_used_bytes` — real bytes.
- To find what an image actually costs, enable storage inspection (section 6)
  and use `crio_image_exclusive_size_bytes` — bytes reclaimed if you delete it.
- `crio_image_deduplication_ratio` tells you how inflated apparent sizes are
  on a given node.

Full detail, including the label-join pattern every per-image query needs, is
in [`docs/METRICS.md`](docs/METRICS.md).

## 4. Metrics

Headline metrics — see [`docs/METRICS.md`](docs/METRICS.md) for the complete
list (25 metrics across per-image, join, node-aggregate and exporter-health
tables), with types, labels, and which require storage inspection or image
age collection.

| Metric | Type | Meaning |
|---|---|---|
| `crio_image_size_bytes` | gauge | Apparent per-image size. Not deduplicated — see section 3. |
| `crio_image_info` | gauge | Repo tag / digest metadata for a `image_id`. Join target, see below. |
| `crio_image_filesystem_used_bytes` | gauge | Real, deduplicated bytes used on the image filesystem. Alert on this. |
| `crio_image_deduplication_ratio` | gauge | How inflated apparent sizes are on this node. |
| `crio_image_exclusive_size_bytes` | gauge | Bytes reclaimed by deleting this image. Requires storage inspection. |
| `crio_image_exporter_scrape_success` | gauge | `1` if the last scrape's CRI calls all succeeded. |

Per-image metrics carry only an `image_id` label. Names live in
`crio_image_info`, so join before you look at anything by name. Note the
operand order: `crio_image_info` emits one series per repo tag, so it is the
"many" side and must be on the right of `group_right` — writing
`group_left(repository, tag)` with `crio_image_info` on the right looks
plausible, works for single-tag images, and fails on every multi-tag image
with "found duplicate series for the match group".

```promql
crio_image_size_bytes * on(image_id) group_right() crio_image_info
```

Across multiple nodes, join on `instance` too:

```promql
crio_image_size_bytes * on(instance, image_id) group_right() crio_image_info
```

## 5. Configuration

Every flag has a matching `CRIO_IMAGE_EXPORTER_`-prefixed environment
variable — `--max-images` is `CRIO_IMAGE_EXPORTER_MAX_IMAGES`. Precedence is
flag > environment > default.

| Flag | Env var | Default | Description |
|---|---|---|---|
| `--cri-socket` | `CRIO_IMAGE_EXPORTER_CRI_SOCKET` | `unix:///var/run/crio/crio.sock` | CRI-O gRPC endpoint. |
| `--listen-address` | `CRIO_IMAGE_EXPORTER_LISTEN_ADDRESS` | `127.0.0.1:8080` | Address to serve metrics on. |
| `--metrics-path` | `CRIO_IMAGE_EXPORTER_METRICS_PATH` | `/metrics` | Path to serve metrics on. |
| `--cri-timeout` | `CRIO_IMAGE_EXPORTER_CRI_TIMEOUT` | `10s` | Per-RPC timeout. |
| `--collect-image-age` | `CRIO_IMAGE_EXPORTER_COLLECT_IMAGE_AGE` | `true` | Collect image creation timestamps. |
| `--image-age-refresh-interval` | `CRIO_IMAGE_EXPORTER_IMAGE_AGE_REFRESH_INTERVAL` | `5m` | Image age cache refresh interval. |
| `--storage-root` | `CRIO_IMAGE_EXPORTER_STORAGE_ROOT` | `""` | `containers/storage` root for exact layer attribution; empty disables (see section 6). |
| `--storage-refresh-interval` | `CRIO_IMAGE_EXPORTER_STORAGE_REFRESH_INTERVAL` | `5m` | Storage graph refresh interval. |
| `--image-name-filter` | `CRIO_IMAGE_EXPORTER_IMAGE_NAME_FILTER` | `""` | Regex allowlist matched against repo tags; empty allows all. |
| `--max-images` | `CRIO_IMAGE_EXPORTER_MAX_IMAGES` | `0` | Cap on per-image series; `0` is unlimited. |
| `--disable-per-image` | `CRIO_IMAGE_EXPORTER_DISABLE_PER_IMAGE` | `false` | Emit aggregates and health metrics only. |
| `--log-level` | `CRIO_IMAGE_EXPORTER_LOG_LEVEL` | `info` | Log level: `debug`, `info`, `warn`, `error`. |

The binary also serves `/healthz` (always `200`) and `/readyz` (`200` once
the CRI connection has been established at least once, and sticky
thereafter — a later CRI hiccup shows up as `crio_image_exporter_scrape_success=0`,
not as the pod leaving service).

## 6. Exact size attribution

By default the exporter can only report *apparent* per-image sizes (section
3), because CRI's image config carries layer identity but not per-layer byte
sizes. Setting `--storage-root` (chart: `storageInspection.enabled=true`)
turns on a second, optional data source: the exporter reads
`containers/storage`'s own `layers.json` and `images.json` directly, builds
the layer-sharing graph, and computes exactly how many bytes each image would
free up if deleted.

What it buys: `crio_image_exclusive_size_bytes` and
`crio_image_shared_size_bytes` per image, and a node-level
`crio_images_deduplicated_size_bytes_total` that should closely track
`crio_image_filesystem_used_bytes`.

What it costs:

- A second read-only hostPath mount of the storage root, with the same
  SELinux exposure as the CRI socket (section 7).
- Coupling to `containers/storage`'s on-disk layout, which is an
  implementation detail, not a stable API contract. A storage format change
  can break parsing.

Because of that coupling, storage inspection degrades to *absent* metrics
rather than a broken exporter: a parse failure retains the previous snapshot,
increments `crio_image_exporter_storage_errors_total`, and never blanks or
zeroes the size metrics. Everything else — apparent sizes, node aggregates,
`crio_image_deduplication_ratio` — keeps working with `--storage-root` unset.

Turn it on when apparent sizes and the deduplication ratio are not enough —
typically when you need to answer "what do I reclaim if I delete image X" for
real, e.g. before pruning images on disk-pressured nodes.

```bash
helm upgrade crio-image-exporter ./charts/crio-image-exporter \
  --set storageInspection.enabled=true
```

## 7. OpenShift and SELinux

The chart binds the DaemonSet's ServiceAccount to the builtin
`hostmount-anyuid` SCC (it creates no SCC object of its own). That grants
hostPath volumes and an arbitrary UID, but it does **not** relax SELinux —
and on OpenShift a pod's default `container_t` process type is typically
denied a connection to `crio.sock`.

If pods log something like:

```
connect to CRI: permission denied
```

(a `permission denied` while dialing the socket), SELinux is the cause.
Escalate one rung at a time:

1. **`hostmount-anyuid`, no `seLinuxOptions`** (the chart default). Grants
   hostPath mounts and UID 0, nothing about SELinux type.
2. **Relax the SELinux type on the pod:**
   ```bash
   helm upgrade crio-image-exporter ./charts/crio-image-exporter \
     --set 'podSecurityContext.seLinuxOptions.type=spc_t'
   ```
   `spc_t` ("super privileged container") is unconfined with respect to
   SELinux while every other constraint (capabilities, UID, read-only
   rootfs) stays in place.
3. **Move to the `privileged` SCC** if rung 2 is not enough:
   ```bash
   helm upgrade crio-image-exporter ./charts/crio-image-exporter \
     --set scc.name=privileged
   ```

Try each rung only after confirming the symptom above; do not start at
`privileged` by default.

## 8. Cardinality

Per-image series (`crio_image_size_bytes`, `crio_image_info`,
`crio_image_pinned`, `crio_image_containers`, `crio_image_created_timestamp_seconds`,
and, with storage inspection, `crio_image_exclusive_size_bytes` /
`crio_image_shared_size_bytes`) are **on by default**. On a large fleet this
is the dominant source of series.

Rough series count per node, before tag fan-out on `crio_image_info`:

```
images_per_node × 6 + tags
```

(six per-image metrics, plus one `crio_image_info` series per repo tag —
most images carry one tag, some carry none, a few carry several).

Three brakes, in order of bluntness:

| Flag | Effect |
|---|---|
| `--image-name-filter` | Regex allowlist matched against repo tags; only matching images get per-image series. |
| `--max-images` | Caps per-image series at N; the largest images by apparent size are kept, the rest are counted in `crio_image_exporter_images_truncated`. |
| `--disable-per-image` | Drops all per-image series; only node aggregates and exporter health remain. |

Node aggregates (`crio_images_total`, `crio_image_filesystem_used_bytes`,
`crio_image_deduplication_ratio`, etc.) are computed over *every* image
regardless of these controls, so totals stay accurate even at
`--disable-per-image`.

## 9. Example queries

Top reclaimable images across the fleet (requires storage inspection):

```promql
topk(10, crio_image_exclusive_size_bytes)
  * on(instance, image_id) group_right() crio_image_info
```

Unreferenced images — present on disk, used by no running container:

```promql
crio_image_size_bytes * on(instance, image_id) group_right() crio_image_info
  and on(instance, image_id) crio_image_containers == 0
```

Images older than 30 days:

```promql
(time() - crio_image_created_timestamp_seconds > 30 * 86400)
  * on(instance, image_id) group_right() crio_image_info
```

Nodes whose real image disk usage is growing (over the last 6 hours):

```promql
delta(crio_image_filesystem_used_bytes[6h]) > 0
```

## 10. Development

```bash
make test    # go test ./... -race -count=1
make build   # binary at bin/crio-image-exporter
make image   # container image via podman; see Containerfile
```

`make vet` and `make helm-lint` run `go vet` and `helm lint` respectively.
The container image has not been published anywhere — building it locally
with `make image` requires a working `podman` (or compatible) installation.

## License

Apache License 2.0 — see [`LICENSE`](LICENSE).
