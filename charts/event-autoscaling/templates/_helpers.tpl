{{/* Base name for every resource: the release name unless overridden. */}}
{{- define "ea.fullname" -}}
{{- default .Release.Name .Values.fullnameOverride | trunc 50 | trimSuffix "-" }}
{{- end }}

{{- define "ea.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/* Common labels. Call with (dict "ctx" $ "component" "api"). */}}
{{- define "ea.labels" -}}
helm.sh/chart: {{ include "ea.chart" .ctx }}
{{ include "ea.selectorLabels" . }}
app.kubernetes.io/version: {{ include "ea.imageTag" .ctx | quote }}
app.kubernetes.io/managed-by: {{ .ctx.Release.Service }}
app.kubernetes.io/part-of: k8s-event-driven-autoscaling
{{- end }}

{{- define "ea.selectorLabels" -}}
app.kubernetes.io/name: {{ .ctx.Chart.Name }}
app.kubernetes.io/instance: {{ .ctx.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end }}

{{- define "ea.imageTag" -}}
{{- default .Chart.AppVersion .Values.image.tag }}
{{- end }}

{{- define "ea.image" -}}
{{- $ref := printf "%s:%s" .Values.image.repository (include "ea.imageTag" .) }}
{{- if .Values.image.digest }}
{{- $ref = printf "%s@%s" $ref .Values.image.digest }}
{{- end }}
{{- $ref }}
{{- end }}

{{/* host:port of Redis: the in-chart instance, or redis.address for an external one. */}}
{{- define "ea.redisAddress" -}}
{{- if .Values.redis.address }}
{{- .Values.redis.address }}
{{- else if .Values.redis.enabled }}
{{- printf "%s-redis.%s.svc.cluster.local:6379" (include "ea.fullname" .) .Release.Namespace }}
{{- else }}
{{- fail "redis.address is required when redis.enabled is false" }}
{{- end }}
{{- end }}

{{/* Topology spread across zones and nodes. Call with (dict "ctx" $ "component" "api"). */}}
{{- define "ea.topologySpread" -}}
{{- $ts := .ctx.Values.topologySpread }}
{{- if or $ts.zone.enabled $ts.hostname.enabled -}}
# nodeTaintsPolicy Honor: a cordoned (draining) node is not counted as a
# domain, so DoNotSchedule cannot deadlock replacement pods during a drain.
topologySpreadConstraints:
  {{- range $key, $c := (dict "topology.kubernetes.io/zone" $ts.zone "kubernetes.io/hostname" $ts.hostname) }}
  {{- if $c.enabled }}
  - topologyKey: {{ $key }}
    maxSkew: {{ $c.maxSkew }}
    whenUnsatisfiable: {{ $c.whenUnsatisfiable }}
    nodeTaintsPolicy: Honor
    matchLabelKeys:
      - pod-template-hash
    labelSelector:
      matchLabels:
        {{- include "ea.selectorLabels" (dict "ctx" $.ctx "component" $.component) | nindent 8 }}
  {{- end }}
  {{- end }}
{{- end }}
{{- end }}

{{- define "ea.containerSecurityContext" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
runAsNonRoot: true
capabilities:
  drop: ["ALL"]
{{- end }}
