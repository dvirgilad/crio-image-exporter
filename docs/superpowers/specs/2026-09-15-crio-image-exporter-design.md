# crio-image-exporter — Design

Date: 2026-09-15
Module: `github.com/dvirgilad/crio-image-exporter`
Status: Approved

## Problem

Operators running CRI-O on OpenShift have no per-node visibility into which
container images occupy disk on their nodes. Node disk pressure shows up as a
symptom (`DiskPressure` taints, evictions) with no attribution: which images
are large, which are unreferenced, which are pinned and therefore exempt from
garbage collection.

This exporter answers those questions as Prometheus metrics, one instance per
node, deployed as a DaemonSet.

## Goals

- Per-image size attribution on every node.
- True image-filesystem disk usage, distinct from summed image sizes.
- Identify unreferenced images (no containers) and pinned images (GC-exempt).
- Image age, for stale-image GC policy decisions.
- Deploy cleanly on OpenShift under a builtin SCC, no custom SCC object.
- Scrape via OpenShift user-workload monitoring over TLS.

## Non-Goals

- Layer-level deduplication accounting. CRI does not expose it; obtaining it
  would require reading `containers/storage` internals directly. Out of scope.
- Image vulnerability or content inspection.
- Mutating anything. The exporter is strictly read-only against CRI-O.
- Cluster-wide aggregation. That is Prometheus's job, not the exporter's.

## Architecture

A single Go binary. One DaemonSet pod per node. Each pod dials its own node's
CRI-O socket over a read-only `hostPath` mount.

```
Prometheus ──scrape(TLS)──> kube-rbac-proxy :9443 ──> exporter :8080 (127.0.0.1)
                                                          │
                            scrape path (synchronous) ────┼─ ListImages()
                                                          ├─ ImageFsInfo()
                                                          ├─ ListContainers()
                                                          └─ Version()
                                                          │
                            background (every 5m) ────────┴─ ImageStatus(verbose)
                                                          │
                                              unix:///var/run/crio/crio.sock (ro)
```

The scrape path is synchronous and stateless: Prometheus scrapes, the collector
issues four RPCs, builds metrics, returns. No cache, no staleness, no
background work on the critical path.

The single exception is the image-age cache (see "Image age"), which is the
only mutable state in the process.

### Packages

| Package | Responsibility |
|---|---|
| `cmd/crio-image-exporter` | Flag parsing, wiring, HTTP server, signal handling. |
| `internal/config` | Config struct, flag/env binding, validation. |
| `internal/cri` | `Client` interface, real gRPC implementation, `Fake` for tests. |
| `internal/collector` | `prometheus.Collector` implementation. Depends only on `cri.Client`. |
| `internal/agecache` | Background image-age refresh loop and cache. |

The collector depends on the `cri.Client` interface only. Every collector test
runs against `cri.Fake` — no containers, no sockets, no privileges.

## Metric shape

A CRI image has one ID and zero or more repo tags. Denormalizing tags into the
size metric's labels means a three-tag image emits three series each carrying
the full size, and `sum()` silently triple-counts.

Therefore size is keyed on identity alone, and human-readable names live in a
separate join metric:

```
crio_image_size_bytes{image_id="sha256:ab…"} 42949672960
crio_image_info{image_id="sha256:ab…", repository="quay.io/foo/bar", tag="v1", digest="sha256:cd…"} 1
```

Joined in PromQL:

```promql
crio_image_size_bytes * on(image_id) group_left(repository, tag) crio_image_info
```

This is less pleasant to query and is correct under aggregation. The
denormalized alternative is pleasant and wrong.

Images with no repo tags emit `crio_image_info` with `repository="<none>",
tag="<none>"` so they remain joinable and visible.

## Metrics

### Per-image — labels: `image_id`

| Metric | Type | Meaning |
|---|---|---|
| `crio_image_size_bytes` | gauge | Apparent image size reported by CRI. **Not** deduplicated across shared layers. |
| `crio_image_pinned` | gauge | `1` if pinned (exempt from CRI-O image GC), else `0`. |
| `crio_image_containers` | gauge | Number of containers currently referencing this image. `0` means removable. |
| `crio_image_created_timestamp_seconds` | gauge | Image creation time, Unix seconds. Absent if unavailable. |

### Join metric

| Metric | Type | Labels |
|---|---|---|
| `crio_image_info` | gauge (always `1`) | `image_id`, `repository`, `tag`, `digest` |

One series per repo tag. An image with three tags yields three `crio_image_info`
series and exactly one `crio_image_size_bytes` series.

### Node aggregate

| Metric | Type | Meaning |
|---|---|---|
| `crio_images_total` | gauge | Image count on this node. |
| `crio_images_apparent_size_bytes_total` | gauge | Sum of apparent image sizes. **Not disk usage.** |
| `crio_image_filesystem_used_bytes` | gauge | Real bytes used on the image filesystem. Label: `filesystem`. |
| `crio_image_filesystem_inodes_used` | gauge | Inodes used on the image filesystem. Label: `filesystem`. |
| `crio_containers_total` | gauge | Container count. Label: `state`. |

### Exporter health

| Metric | Type | Meaning |
|---|---|---|
| `crio_image_exporter_scrape_duration_seconds` | gauge | Duration of the last scrape. |
| `crio_image_exporter_scrape_success` | gauge | `1` if all scrape-path RPCs succeeded. |
| `crio_image_exporter_cri_errors_total` | counter | CRI RPC failures. Label: `rpc`. |
| `crio_image_exporter_images_truncated` | gauge | Images omitted due to `--max-images`. |
| `crio_image_exporter_age_cache_entries` | gauge | Entries in the image-age cache. |
| `crio_image_exporter_age_refresh_timestamp_seconds` | gauge | Unix time of last successful age refresh. |
| `crio_image_exporter_build_info` | gauge | Always `1`. Labels: `version`, `revision`, `go_version`. |
| `crio_runtime_info` | gauge | Always `1`. Labels: `runtime_name`, `runtime_version`, `api_version`. |

### The size caveat

Image sizes reported by CRI do not account for shared layers. Ten images built
on the same base each report the base layer's bytes. Consequently
`crio_images_apparent_size_bytes_total` substantially exceeds real disk usage
and must never be used for disk-pressure alerting.

- Alert on `crio_image_filesystem_used_bytes` — real disk.
- Use per-image sizes for *relative* comparison — which image is large.

This caveat is stated prominently in `README.md` and `docs/METRICS.md`.

## Image age

CRI's `Image` message carries no creation timestamp. Obtaining one requires
`ImageStatus(verbose=true)` **per image** and parsing CRI-O's runtime-specific
`info` JSON blob, which is not part of the stable CRI contract.

A background goroutine maintains an `imageID → createdTimestamp` cache. Because
creation time is immutable for a given image ID, the refresh loop only issues
`ImageStatus` for IDs it has not seen before:

```
every --image-age-refresh-interval (default 5m):
    ids := ListImages()
    for each id not in cache:
        cache[id] = parseCreated(ImageStatus(id, verbose=true))
    prune cache entries whose id is no longer present
```

Cost is N RPCs at startup and approximately zero in steady state — only newly
pulled images. The scrape path reads the cache under an `RWMutex` and never
blocks on CRI.

Accepted consequences:

- A freshly pulled image has no age metric until the next refresh, a gap of up
  to one interval. Acceptable for an age metric.
- Parsing a non-standard verbose blob is fragile. Failures degrade to "metric
  absent for that image" and increment `crio_image_exporter_cri_errors_total`.
  They never fail a scrape.

Enabled by default (`--collect-image-age=false` to disable), since the cost is
amortized.

## Cardinality control

Per-image series are enabled by default. Brakes, in order of bluntness:

| Flag | Default | Effect |
|---|---|---|
| `--image-name-filter` | `""` | Regex allowlist matched against repo tags. Empty means all. |
| `--max-images` | `0` | Cap on per-image series; `0` is unlimited. Excess images are omitted and counted in `crio_image_exporter_images_truncated`. When capping, images are sorted by size descending so the largest survive. |
| `--disable-per-image` | `false` | Aggregates and health metrics only. |

Node aggregates are computed over *all* images regardless of filtering, so
totals stay accurate when per-image series are suppressed.

## Configuration

Every flag has a matching `CRIO_IMAGE_EXPORTER_`-prefixed environment variable.

| Flag | Default |
|---|---|
| `--cri-socket` | `unix:///var/run/crio/crio.sock` |
| `--listen-address` | `127.0.0.1:8080` |
| `--metrics-path` | `/metrics` |
| `--cri-timeout` | `10s` |
| `--collect-image-age` | `true` |
| `--image-age-refresh-interval` | `5m` |
| `--image-name-filter` | `""` |
| `--max-images` | `0` |
| `--disable-per-image` | `false` |
| `--log-level` | `info` |

## Error handling

Partial failure must not produce an empty scrape. Each scrape-path RPC is
independent: if `ImageFsInfo` fails, image metrics are still emitted, the
filesystem metrics are omitted, `crio_image_exporter_cri_errors_total{rpc="ImageFsInfo"}`
increments, and `crio_image_exporter_scrape_success` is set to `0`.

A completely unreachable socket yields a scrape containing only health metrics,
with `scrape_success=0` — deliberately a successful HTTP 200, so Prometheus
records the failure signal rather than treating the target as down.

The process does not exit on CRI errors. It exits non-zero only on invalid
configuration at startup.

Health endpoints: `/healthz` (process liveness, always 200 once serving) and
`/readyz` (200 once the CRI connection has succeeded at least once).

## Deployment

### DaemonSet

Two containers:

- **exporter** — binds `127.0.0.1:8080`, never reachable from outside the pod.
- **kube-rbac-proxy** — listens on `:9443` with TLS, authenticates and
  authorizes scrapers against the Kubernetes API, forwards to the exporter.

Volumes: the CRI socket, mounted `readOnly: true`; the serving-cert secret.

Tolerations default to running everywhere, including control-plane nodes, since
disk attribution matters most there. Configurable.

### Security context

```yaml
runAsUser: 0                      # crio.sock is root-owned, mode 0660
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities: { drop: ["ALL"] }
seccompProfile: { type: RuntimeDefault }
```

UID 0 is required to read the socket; every other privilege is dropped.

### SCC

The chart creates no SCC. It creates a `Role`/`RoleBinding` granting the
ServiceAccount `use` on the builtin `hostmount-anyuid` SCC, which permits
`hostPath` volumes and an arbitrary UID.

**Known risk — SELinux.** `hostmount-anyuid` does not relax SELinux. On
OpenShift, `crio.sock` is labeled such that a default `container_t` process is
typically denied connecting to it. This is surfaced as configuration rather
than assumed away:

1. Default: `hostmount-anyuid`, no `seLinuxOptions`.
2. If the pod logs `permission denied` dialing the socket, set
   `securityContext.seLinuxOptions.type: spc_t` in `values.yaml`.
3. If that still fails, bind the `privileged` SCC via `scc.name` in
   `values.yaml`.

The README documents this escalation ladder explicitly, including why each rung
exists, so operators grant the least privilege that actually works rather than
starting at `privileged`.

### Monitoring integration

- `Service` annotated with `service.beta.openshift.io/serving-cert-secret-name`
  so OpenShift issues the serving certificate.
- `ServiceMonitor` referencing the service CA bundle, for OpenShift
  user-workload monitoring.
- Both are toggleable for non-OpenShift clusters, where the chart falls back to
  a plaintext port and no proxy sidecar.

## Testing

Test-driven, against `cri.Fake`. No test requires a container runtime, a
socket, or elevated privileges.

Collector cases:

- Single image, single tag — the baseline.
- Image with three repo tags — one size series, three info series.
- Untagged image — `<none>` labels, still joinable.
- Zero images — aggregates present and zero, no per-image series.
- Per-RPC failure — each of the four scrape RPCs failing independently still
  yields metrics for the others, with the correct error counter and
  `scrape_success=0`.
- `--max-images` truncation — largest images retained, truncation gauge set.
- `--image-name-filter` — filtering applies to per-image series but not to
  aggregates.
- `--disable-per-image` — no per-image series emitted.

Age cache cases:

- Cold start populates all entries.
- Second refresh issues no `ImageStatus` calls for already-cached IDs.
- Removed images are pruned.
- Malformed verbose blob yields an absent metric and an incremented error
  counter, not a panic.

A golden-output test using `testutil.CollectAndCompare` pins the exact exposed
format of the full registry, so metric renames become deliberate, visible diffs.

## Repository layout

```
README.md
docs/METRICS.md
docs/superpowers/specs/2026-09-15-crio-image-exporter-design.md
Containerfile
Makefile
go.mod
cmd/crio-image-exporter/main.go
internal/config/config.go
internal/cri/{client.go,fake.go,types.go}
internal/collector/{collector.go,collector_test.go}
internal/agecache/{cache.go,cache_test.go}
charts/crio-image-exporter/
    Chart.yaml
    values.yaml
    README.md
    templates/{_helpers.tpl,daemonset.yaml,service.yaml,serviceaccount.yaml,
               servicemonitor.yaml,scc-role.yaml,rbac.yaml,NOTES.txt}
dashboards/crio-images.json
```

Container image: UBI9-micro base, multi-arch `linux/amd64` and `linux/arm64`,
static binary, non-layered.

No CI in the initial build.

## Open questions

None.
