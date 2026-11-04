{{/*
Expand the name of the chart.
*/}}
{{- define "cloud-vinyl.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
*/}}
{{- define "cloud-vinyl.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart label.
*/}}
{{- define "cloud-vinyl.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "cloud-vinyl.labels" -}}
helm.sh/chart: {{ include "cloud-vinyl.chart" . }}
{{ include "cloud-vinyl.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "cloud-vinyl.selectorLabels" -}}
app.kubernetes.io/name: {{ include "cloud-vinyl.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
ServiceAccount name.
*/}}
{{- define "cloud-vinyl.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "cloud-vinyl.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Operator image reference (repository:tag, falling back to appVersion).
*/}}
{{- define "cloud-vinyl.operatorImage" -}}
{{- $tag := .Values.image.operator.tag | default .Chart.AppVersion }}
{{- printf "%s:%s" .Values.image.operator.repository $tag }}
{{- end }}

{{/*
Webhook TLS secret name.
*/}}
{{- define "cloud-vinyl.webhookTLSSecret" -}}
{{- printf "%s-webhook-tls" (include "cloud-vinyl.fullname" .) }}
{{- end }}

{{/*
Metrics serving certificate Secret, or empty when the operator generates a
self-signed certificate itself.
*/}}
{{- define "cloud-vinyl.metricsTLSSecret" -}}
{{- $tls := .Values.monitoring.metrics.tls -}}
{{- if $tls.secretName -}}
{{- $tls.secretName -}}
{{- else if $tls.certManager -}}
{{- printf "%s-metrics-tls" (include "cloud-vinyl.fullname" .) -}}
{{- end -}}
{{- end }}

{{/*
In-cluster DNS name of the Service carrying the metrics port.
*/}}
{{- define "cloud-vinyl.metricsServiceHost" -}}
{{- printf "%s.%s.svc" (include "cloud-vinyl.fullname" .) .Release.Namespace -}}
{{- end }}

{{/*
Effective runtime catalog (the content of runtimes.yaml). Fills an empty tracer
with the tracer of this chart release (<appVersion> for varnish,
<appVersion>-vinyl for vinyl). While the deprecated image.tracer.repository or
image.tracer.tag is set, it replaces the tracer of every varnish entry.
*/}}
{{- define "cloud-vinyl.runtimes" -}}
{{- if not .Values.runtimes -}}
{{- fail "cloud-vinyl: the chart's runtime catalog (values key `runtimes`) is missing. This typically happens with `helm upgrade --reuse-values` across the upgrade to 1.1, which keeps the old values and drops the new defaults. Use `--reset-then-reuse-values` (Helm 3.14+) or pass the values again." -}}
{{- end -}}
{{- $rt := deepCopy .Values.runtimes -}}
{{- $base := "ghcr.io/bluedynamics/cloud-vinyl-tracer" -}}
{{- $legacy := or .Values.image.tracer.repository .Values.image.tracer.tag -}}
{{- range $flavor, $spec := $rt -}}
{{- if not (hasKey (default dict $spec.versions) (toString $spec.default)) -}}
{{- fail (printf "cloud-vinyl: runtimes.%s.default %q is missing or not among runtimes.%s.versions; the operator would refuse to start" $flavor (toString $spec.default) $flavor) -}}
{{- end -}}
{{- range $version, $entry := $spec.versions -}}
{{- if not $entry.daemon -}}
{{- fail (printf "cloud-vinyl: runtimes.%s.versions.%q has no daemon image; the operator would refuse to start" $flavor $version) -}}
{{- end -}}
{{- if not $entry.exporter -}}
{{- fail (printf "cloud-vinyl: runtimes.%s.versions.%q has no exporter image; the operator would refuse to start" $flavor $version) -}}
{{- end -}}
{{- if and (eq $flavor "varnish") $legacy -}}
{{- $_ := set $entry "tracer" (printf "%s:%s" ($.Values.image.tracer.repository | default $base) ($.Values.image.tracer.tag | default $.Chart.AppVersion)) -}}
{{- else if not $entry.tracer -}}
{{- $suffix := ternary "-vinyl" "" (eq $flavor "vinyl") -}}
{{- $_ := set $entry "tracer" (printf "%s:%s%s" $base $.Chart.AppVersion $suffix) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- toYaml $rt -}}
{{- end }}
