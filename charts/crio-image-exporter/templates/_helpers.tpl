{{- define "crio-image-exporter.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "crio-image-exporter.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name (include "crio-image-exporter.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "crio-image-exporter.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{ include "crio-image-exporter.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "crio-image-exporter.selectorLabels" -}}
app.kubernetes.io/name: {{ include "crio-image-exporter.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "crio-image-exporter.serviceAccountName" -}}
{{ include "crio-image-exporter.fullname" . }}
{{- end -}}

{{/*
The address the exporter binds. Loopback when kube-rbac-proxy fronts it, so
only the sidecar can reach it; otherwise the pod IP. Shared by the container
args and the exec health probes so the two can never drift.
*/}}
{{- define "crio-image-exporter.listenAddress" -}}
{{- if .Values.kubeRBACProxy.enabled -}}
127.0.0.1:8080
{{- else -}}
0.0.0.0:8080
{{- end -}}
{{- end -}}
