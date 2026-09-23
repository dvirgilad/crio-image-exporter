# Metrics reference

## Read this before using image sizes

`crio_image_size_bytes` is an **apparent** size. It does not account for layers
shared between images: ten images built on the same base each report that
base's bytes. Summing apparent sizes therefore overstates disk, often by 3x or
more.

- To alert on disk, use `crio_image_filesystem_used_bytes` — real bytes.
- To find what an image actually costs, enable storage inspection and use
  `crio_image_exclusive_size_bytes` — bytes reclaimed if you delete it.
- `crio_image_deduplication_ratio` tells you how inflated apparent sizes are on
  a given node.

## The node label

Every metric this exporter publishes carries a `node` label naming the node it
describes, including the Go runtime and process metrics. The DaemonSet fills it
in from the downward API (`spec.nodeName`) via
`CRIO_IMAGE_EXPORTER_NODE_NAME`.

Prefer it to `instance` when grouping or joining. `instance` is the pod's
`IP:port`, so it changes every time the pod is rescheduled and breaks the
continuity of a series across a rollout; `node` does not. Both work in the
joins below -- the examples use `instance` because it is present whether or not
the exporter knows its node name.

Outside Kubernetes, with no `--node-name` given, the label is omitted entirely
rather than emitted empty: `node=""` would read as a node whose name is
genuinely blank.

## Joining names onto per-image metrics

Per-image metrics are labeled with `image_id` only. Names live in
`crio_image_info`, because an image can carry several repo tags and putting
them on the size metric would make `sum()` count that image once per tag.

    crio_image_size_bytes * on(instance, image_id) group_right() crio_image_info

`crio_image_size_bytes` emits one series per `image_id`; `crio_image_info`
emits one series per repo tag, so it is the "many" side of the match and
belongs on the right of `group_right`. The result inherits `crio_image_info`'s
full label set, so `repository`, `tag` and `digest` come through without
needing to be named in the modifier. Getting this backwards
(`group_left(repository, tag)` with `crio_image_info` on the right) works for
single-tag images and fails with "found duplicate series for the match group"
on any multi-tag image — exactly the case this join exists for.

`instance` is in the matcher because this exporter is a DaemonSet: the same
`image_id` appears once per node, and matching on `image_id` alone makes the
left side many-to-one for the same reason described above. Drop `instance`
only when the query is already scoped to a single node:

    crio_image_size_bytes * on(image_id) group_right() crio_image_info

---

## Per-image metrics

One series per image (subject to `--image-name-filter` / `--max-images` /
`--disable-per-image`; see [Cardinality](../README.md#8-cardinality) in the
README). Labeled with `image_id` unless noted.

| Metric | Type | Labels | Requires | Description |
|---|---|---|---|---|
| `crio_image_size_bytes` | gauge | `image_id` | — | Apparent size of the image, as reported by CRI. **Not** deduplicated across shared layers — see the warning above. |
| `crio_image_pinned` | gauge | `image_id` | — | `1` if the image is exempt from CRI-O's image garbage collection, else `0`. |
| `crio_image_containers` | gauge | `image_id` | — | Number of containers currently referencing this image. `0` means the image is removable. Absent for all images if container listing failed for that scrape (not the same as `0`). |
| `crio_image_created_timestamp_seconds` | gauge | `image_id` | `--collect-image-age` (default on) | Image creation time in Unix seconds. Absent until the age cache has resolved it, or if CRI-O's info blob could not be parsed. |
| `crio_image_exclusive_size_bytes` | gauge | `image_id` | `--storage-root` (storage inspection) | Bytes reclaimed if this image is deleted — layers referenced by no other image. The honest answer to "what does deleting this cost me back?" Absent, not zero, for images missing from the storage graph. |
| `crio_image_shared_size_bytes` | gauge | `image_id` | `--storage-root` (storage inspection) | Bytes in layers this image shares with at least one other image. |

## Join metric

Names, tags and digests, separated from size so that `sum()` over per-image
metrics is not multiplied by tag count. Always emitted, even for untagged
images.

| Metric | Type | Labels | Requires | Description |
|---|---|---|---|---|
| `crio_image_info` | gauge, always `1` | `image_id`, `repository`, `tag`, `digest` | — | Image name metadata. One series per repo tag; an image with three tags yields three series here and exactly one `crio_image_size_bytes` series. An image with no repo tags gets one series with `repository="<none>"`, `tag="<none>"` so it stays joinable. |

## Node aggregate metrics

Computed over *every* image on the node, regardless of per-image filtering —
so these totals stay accurate even with `--disable-per-image`.

| Metric | Type | Labels | Requires | Description |
|---|---|---|---|---|
| `crio_images_total` | gauge | — | — | Number of images present on the node. |
| `crio_images_apparent_size_bytes_total` | gauge | — | — | Sum of apparent image sizes. **Not disk usage** — see `crio_image_filesystem_used_bytes`. |
| `crio_image_filesystem_used_bytes` | gauge | `filesystem` | — | Real, deduplicated bytes used on the image filesystem, from `ImageFsInfo`. This is the number to alert on. |
| `crio_image_filesystem_inodes_used` | gauge | `filesystem` | — | Inodes used on the image filesystem. |
| `crio_containers_total` | gauge | `state` | — | Number of containers on the node, by state. |
| `crio_images_deduplicated_size_bytes_total` | gauge | — | `--storage-root` (storage inspection) | Sum of distinct layer sizes on the node — real bytes held by images. Should closely track `crio_image_filesystem_used_bytes`; a persistent gap between the two means storage holds layers no image references (orphans from interrupted pulls). |
| `crio_image_deduplication_ratio` | gauge | — | — | `crio_images_apparent_size_bytes_total / crio_image_filesystem_used_bytes`. Values above 1 mean per-image apparent sizes overstate real disk on this node — this is the number that tells you how much to distrust `crio_image_size_bytes` here. Absent when either half is unavailable or filesystem bytes are `0`, never emitted as `+Inf`. |

## Exporter health metrics

Report the exporter's own behavior, not CRI-O state. Scrape these to know
whether the other tables can be trusted.

| Metric | Type | Labels | Requires | Description |
|---|---|---|---|---|
| `crio_image_exporter_scrape_duration_seconds` | gauge | — | — | Duration of the last scrape. |
| `crio_image_exporter_scrape_success` | gauge | — | — | `1` if every CRI call in the last scrape succeeded, else `0`. The scrape still returns HTTP 200 even when this is `0` — Prometheus needs to see the failure as a value, not as a down target. |
| `crio_image_exporter_cri_errors_total` | counter | `rpc` | — | Total CRI RPC failures, by RPC name (`ListImages`, `ListContainers`, `ImageFsInfo`, `Version`). |
| `crio_image_exporter_images_truncated` | gauge | — | — | Number of images omitted from per-image series by `--max-images`. Node aggregates are unaffected. |
| `crio_image_exporter_age_cache_entries` | gauge | — | `--collect-image-age` (default on) | Number of entries in the image age cache. |
| `crio_image_exporter_age_refresh_timestamp_seconds` | gauge | — | `--collect-image-age` (default on) | Unix time of the last successful image age refresh. |
| `crio_image_exporter_storage_layers` | gauge | — | `--storage-root` (storage inspection) | Number of distinct layers in the parsed storage graph. |
| `crio_image_exporter_storage_refresh_timestamp_seconds` | gauge | — | `--storage-root` (storage inspection) | Unix time of the last successful storage graph parse. |
| `crio_image_exporter_storage_errors_total` | counter | `reason` | `--storage-root` (storage inspection) | Total storage graph parse failures, by reason. Reported even before a first successful parse. A failed parse retains the previous snapshot rather than blanking `crio_image_exclusive_size_bytes` / `crio_image_shared_size_bytes`. |
| `crio_image_exporter_build_info` | gauge, always `1` | `version`, `revision`, `go_version` | — | Exporter build identification. |
| `crio_runtime_info` | gauge, always `1` | `runtime_name`, `runtime_version`, `api_version` | — | Identifies the CRI-O runtime the exporter is talking to on this node. |

## See also

- [`README.md`](../README.md) for deployment, configuration, and example
  queries.
- [`charts/crio-image-exporter/README.md`](../charts/crio-image-exporter/README.md)
  for the Helm values reference.
