# crio-image-exporter

A Prometheus exporter for CRI-O image disk usage, deployed as a DaemonSet on OpenShift and other Kubernetes clusters.

## Overview

`crio-image-exporter` runs as a DaemonSet pod on each node, interrogating the local CRI-O runtime via its Unix socket to report per-image disk usage metrics. On OpenShift it binds the builtin `privileged` SecurityContextConstraint — not to run privileged, but because it is the only builtin SCC permitting the `spc_t` SELinux type the CRI socket requires.

This chart provides:
- **DaemonSet** with secure defaults (read-only root filesystem, dropped capabilities, UID 0 for socket access)
- **Optional kube-rbac-proxy sidecar** for TLS termination and API server-based authorization on OpenShift
- **Optional storage inspection** for exact per-image attribution (vs. apparent sizes)
- **SELinux handling** for the CRI socket, which `container_t` cannot reach (see below)
- **ServiceMonitor** integration for Prometheus Operator, passing the bearer token and CA bundle by object reference

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
| `exporter.metricsPath` | string | `/metrics` | Path the exporter serves metrics on. Also the path kube-rbac-proxy authorizes and the ServiceMonitor scrapes. |
| `storageInspection.enabled` | bool | `false` | Enable exact per-image attribution by reading container storage metadata. |
| `storageInspection.root` | string | `/var/lib/containers/storage` | Container storage root path. |
| `storageInspection.refreshInterval` | string | `5m` | Interval to refresh storage metadata. |
| `scc.create` | bool | `true` | Create RBAC Role and RoleBinding granting use of the builtin SCC named by `scc.name`. |
| `scc.name` | string | `privileged` | Builtin SCC to bind. Must allow `seLinuxContext: RunAsAny` so the pod can request `spc_t`; see SELinux section. |
| `podSecurityContext.seLinuxOptions.type` | string | `spc_t` | SELinux type. Required to reach `crio.sock`; `container_t` is denied. Needs an SCC with `seLinuxContext: RunAsAny`. |
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
| `metricsReader.create` | bool | `true` | Create the ClusterRole and ClusterRoleBinding authorizing scrapers to GET the metrics path. |
| `metricsReader.scrapers` | list | both OpenShift monitoring ServiceAccounts | ServiceAccounts allowed to scrape. Replace on non-OpenShift clusters. |
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

## SELinux and the CRI socket

Reaching `crio.sock` is an SELinux problem, not a file-permission one. The socket is root-owned `0660`, so UID 0 already satisfies the file permissions, but CRI-O's socket is labelled such that a pod's default `container_t` type is denied `connectto`. That MAC denial surfaces as `permission denied` even for root.

Escaping `container_t` means requesting `seLinuxOptions.type: spc_t`, which an SCC permits only when its `seLinuxContext` strategy is `RunAsAny`. Among the builtin SCCs only `privileged` qualifies, so that is what this chart binds, with `spc_t` set by default.

`hostmount-anyuid` cannot work here despite granting hostPath mounts and UID 0: it inherits `restricted`'s `seLinuxContext: MustRunAs`, which validates against the allocated options and rejects a pod requesting `spc_t`.

Binding `privileged` is a permission ceiling, not an instruction. The pod still runs unprivileged, drops every capability, uses a read-only root filesystem and forbids privilege escalation.

Verify what was applied:

```bash
oc get pod -n <ns> -l app.kubernetes.io/name=crio-image-exporter \
  -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.metadata.annotations.openshift\.io/scc}{"\n"}{end}'
```

Under Pod Security Admission the namespace must also permit a custom SELinux type:

```bash
oc label ns <ns> pod-security.kubernetes.io/enforce=privileged --overwrite
```

To avoid binding `privileged`, create a custom SCC that pins `seLinuxOptions.type: spc_t` under `MustRunAs`, set `scc.create=false`, and bind it yourself.

## Prometheus authentication and TLS

When kube-rbac-proxy is enabled, Prometheus must present a bearer token to
scrape the exporter and must verify the proxy's serving certificate. OpenShift's
Prometheus rejects any ServiceMonitor that reaches into the scraper's own
filesystem, so neither `bearerTokenFile` nor `tlsConfig.caFile` can be used
([operator-sdk#7003](https://github.com/operator-framework/operator-sdk/issues/7003)).
A rejected ServiceMonitor is skipped silently: no error on the resource, no
metrics collected.

The chart therefore passes both by reference to objects in the release
namespace, which is what the ServiceMonitor spec permits:

| Reference | Object | Populated by |
|-----------|--------|--------------|
| `bearerTokenSecret` | Secret `<fullname>-token`, type `kubernetes.io/service-account-token` | the token controller, which fills in the `token` key |
| `tlsConfig.ca.configMap` | ConfigMap `<fullname>-ca`, annotated `service.beta.openshift.io/inject-cabundle` | the service CA operator, which fills in `service-ca.crt` |

Both are declared without a data block, since their contents are injected. The
injected CA is the same one that signs the serving certificate requested via the
Service's `service.beta.openshift.io/serving-cert-secret-name` annotation, so the
two cannot drift apart.

The Secret is explicit because Kubernetes 1.24 and later no longer mint a token
Secret for a ServiceAccount automatically. Both objects render only when
`serviceMonitor.enabled` and `kubeRBACProxy.enabled` are both true.

### Scraper authorization

kube-rbac-proxy runs without a config file, so it authorizes every request by
asking the API server, via SubjectAccessReview, whether the caller may `get` the
non-resource URL being requested. A scraper that authenticates successfully but
holds no such grant is refused with **403**, which shows up in Prometheus as a
target stuck `down (403)` rather than as an authentication error.

The chart therefore creates a ClusterRole granting `get` on
`exporter.metricsPath` and binds it to the ServiceAccounts in
`metricsReader.scrapers`. It has to be cluster-scoped: `nonResourceURLs` are not
permitted in a namespaced Role.

Both OpenShift monitoring ServiceAccounts are bound by default, because which
one scrapes depends on the namespace -- cluster monitoring covers `openshift-*`,
user workload monitoring covers everything else. Binding the stack that is not
in play grants nothing. On a plain Kubernetes cluster, point
`metricsReader.scrapers` at your own Prometheus ServiceAccount.

Check a grant directly:

```bash
oc auth can-i get /metrics \
  --as=system:serviceaccount:openshift-user-workload-monitoring:prometheus-user-workload
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

### Target down with 403

kube-rbac-proxy authenticated the scraper and then refused it. The scraper's
ServiceAccount is missing the metrics-reader grant -- see Scraper authorization
above, and confirm with `oc auth can-i`. A 401 instead means the token never
arrived, which is a `bearerTokenSecret` problem, not an RBAC one.

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
- Is authorized to use the `privileged` SCC via RBAC, solely to request the `spc_t` SELinux type.
