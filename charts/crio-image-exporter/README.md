# crio-image-exporter

A Prometheus exporter for CRI-O image disk usage, deployed as a DaemonSet on OpenShift and other Kubernetes clusters.

## Overview

`crio-image-exporter` runs as a DaemonSet pod on each node, interrogating the local CRI-O runtime via its Unix socket to report per-image disk usage metrics. On OpenShift, it requires binding to the builtin `hostmount-anyuid` SecurityContextConstraint.

This chart provides:
- **DaemonSet** with secure defaults (read-only root filesystem, dropped capabilities, UID 0 for socket access)
- **Optional kube-rbac-proxy sidecar** for TLS termination and API server-based authorization on OpenShift
- **Optional storage inspection** for exact per-image attribution (vs. apparent sizes)
- **SELinux escalation path** exposed as values for environments where `container_t` cannot reach `crio.sock`
- **ServiceMonitor** integration for Prometheus Operator

## Installation

Add the repository (when available) and install:

```bash
helm install crio-image-exporter ./charts/crio-image-exporter \
  -n prometheus-system --create-namespace
```

For non-OpenShift clusters, disable kube-rbac-proxy:

```bash
helm install crio-image-exporter ./charts/crio-image-exporter \
  --set kubeRBACProxy.enabled=false
```

## Configuration

### Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `image.repository` | string | `ghcr.io/dvirgilad/crio-image-exporter` | Container image repository. |
| `image.tag` | string | `""` | Container image tag; defaults to `.Chart.AppVersion` if not specified. |
| `image.pullPolicy` | string | `IfNotPresent` | Image pull policy. |
| `imagePullSecrets` | list | `[]` | Image pull secrets for private registries. |
| `nameOverride` | string | `""` | Override the chart name used in generated names. |
| `fullnameOverride` | string | `""` | Override the full name used in generated names. |
| `exporter.logLevel` | string | `info` | Log level (debug, info, warn, error). |
| `exporter.criSocket` | string | `unix:///var/run/crio/crio.sock` | Path to the CRI-O socket as a `unix://` URI. |
| `exporter.criTimeout` | string | `10s` | Timeout for CRI-O API calls. |
| `exporter.collectImageAge` | bool | `true` | Collect image creation timestamps (low overhead, immutable per image ID). |
| `exporter.imageAgeRefreshInterval` | string | `5m` | Interval to refresh image creation timestamps. |
| `exporter.imageNameFilter` | string | `""` | Regex allowlist for image names (repo:tag); empty means all images. |
| `exporter.maxImages` | int | `0` | Maximum per-image series to report; 0 means unlimited. Largest images are retained when capped. |
| `exporter.disablePerImage` | bool | `false` | Disable per-image metrics; only report aggregate metrics. |
| `storageInspection.enabled` | bool | `false` | Enable exact per-image attribution by reading container storage metadata. |
| `storageInspection.root` | string | `/var/lib/containers/storage` | Container storage root path. |
| `storageInspection.refreshInterval` | string | `5m` | Interval to refresh storage metadata. |
| `scc.create` | bool | `true` | Create RBAC Role and RoleBinding for the builtin `hostmount-anyuid` SCC. |
| `scc.name` | string | `hostmount-anyuid` | Name of the builtin SCC to bind (escalate to `privileged` if SELinux is blocking). |
| `podSecurityContext` | object | `{}` | Pod-level security context (e.g., `seLinuxOptions.type: spc_t` if SELinux blocks socket access). |
| `securityContext.runAsUser` | int | `0` | Run as UID 0 (required: `crio.sock` is root-owned mode 0660). |
| `securityContext.allowPrivilegeEscalation` | bool | `false` | Disable privilege escalation. |
| `securityContext.readOnlyRootFilesystem` | bool | `true` | Mount root filesystem as read-only. |
| `securityContext.capabilities.drop` | list | `["ALL"]` | Drop all capabilities. |
| `securityContext.seccompProfile.type` | string | `RuntimeDefault` | Use the default seccomp profile. |
| `service.port` | int | `9443` | Service port for metrics (TLS when kube-rbac-proxy is enabled, plaintext otherwise). |
| `service.servingCertSecretName` | string | `crio-image-exporter-tls` | Name of the serving certificate secret (OpenShift auto-injects it). |
| `kubeRBACProxy.enabled` | bool | `true` | Enable kube-rbac-proxy sidecar for TLS termination and authorization. Set to `false` on non-OpenShift clusters. |
| `kubeRBACProxy.image` | string | `quay.io/brancz/kube-rbac-proxy:v0.18.1` | kube-rbac-proxy container image. |
| `kubeRBACProxy.resources.requests` | object | `{cpu: 10m, memory: 20Mi}` | kube-rbac-proxy resource requests. |
| `kubeRBACProxy.resources.limits` | object | `{memory: 60Mi}` | kube-rbac-proxy resource limits. |
| `serviceMonitor.enabled` | bool | `true` | Create a ServiceMonitor for Prometheus Operator. |
| `serviceMonitor.interval` | string | `60s` | Prometheus scrape interval. |
| `serviceMonitor.scrapeTimeout` | string | `30s` | Prometheus scrape timeout. |
| `serviceMonitor.labels` | object | `{}` | Additional labels for the ServiceMonitor. |
| `resources.requests` | object | `{cpu: 20m, memory: 40Mi}` | Exporter container resource requests. |
| `resources.limits` | object | `{memory: 200Mi}` | Exporter container resource limits. |
| `nodeSelector` | object | `{}` | Node selector for pod placement. |
| `tolerations` | list | `[{operator: Exists}]` | Tolerations to run on all nodes (including control-plane). |
| `affinity` | object | `{}` | Pod affinity rules. |
| `priorityClassName` | string | `""` | Priority class for pod scheduling. |
| `updateStrategy.type` | string | `RollingUpdate` | DaemonSet update strategy. |
| `updateStrategy.rollingUpdate.maxUnavailable` | string | `10%` | Maximum unavailable pods during rolling update. |

## SELinux Escalation Path

If pods log `permission denied` while dialing the CRI socket, SELinux is blocking container_t access to crio.sock. Escalate one rung at a time:

1. **Rung 1 (default):** `hostmount-anyuid` with no `seLinuxOptions`
2. **Rung 2:** Add `podSecurityContext.seLinuxOptions.type: spc_t`
3. **Rung 3:** Change `scc.name` to `privileged`

Apply escalation via:

```bash
# Rung 2: SELinux context permissive
helm upgrade crio-image-exporter ./charts/crio-image-exporter \
  --set 'podSecurityContext.seLinuxOptions.type=spc_t'

# Rung 3: Privileged SCC
helm upgrade crio-image-exporter ./charts/crio-image-exporter \
  --set scc.name=privileged
```

## Storage Inspection

By default, the exporter reports *apparent* image sizes, which double-count layers shared between images. To enable exact per-image attribution (the bytes you reclaim by deleting an image), enable storage inspection:

```bash
helm install crio-image-exporter ./charts/crio-image-exporter \
  --set storageInspection.enabled=true
```

This mounts the container storage root read-only and reads `containers/storage` metadata. Note: this is an on-disk implementation detail, not a stable API.

## Example Metrics Query

Largest reclaimable images on each node:

```promql
topk(10, crio_image_exclusive_size_bytes)
  * on(instance, image_id) group_right() crio_image_info
```

`crio_image_info` emits one series per repo tag, so it is the "many" side of
the match and belongs on the right of `group_right`, which pulls its
`repository`/`tag`/`digest` labels onto the result automatically. The
`group_left(repository, tag)` form works for single-tag images but errors on
any multi-tag image with "found duplicate series for the match group".

This join is necessary because per-image size metrics are labeled only with `image_id`, while human-readable names live in `crio_image_info` (since a single image can have multiple repo:tag labels and summing would multi-count).

## Troubleshooting

### Pods in CrashLoopBackOff

Check pod logs:

```bash
kubectl logs -n prometheus-system -l app.kubernetes.io/name=crio-image-exporter -c exporter
```

Common issues:
- **`flag provided but not defined`**: Chart rendered with incorrect flag names. Verify against exporter's help.
- **`permission denied` dialing socket**: SELinux blocking; escalate as described above.
- **`connection refused`**: CRI-O socket path incorrect; verify `--set exporter.criSocket=...`

### Metrics Not Appearing

1. Confirm ServiceMonitor is created: `kubectl get servicemonitor -n prometheus-system`
2. Check Prometheus targets dashboard for the exporter's service.
3. Verify RBAC: `kubectl describe role <fullname>-auth -n prometheus-system`

## Architecture

Each pod in the DaemonSet:
- Runs the exporter container with read-only root filesystem and dropped capabilities (UID 0 only).
- Optionally runs a kube-rbac-proxy sidecar (OpenShift default) to handle TLS and authorization.
- Mounts `crio.sock` via read-only hostPath.
- Optionally mounts the container storage root for exact attribution.
- Is authorized to use the `hostmount-anyuid` SCC via RBAC.
