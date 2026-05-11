{{/*
Expand the chart name.
*/}}
{{- define "kube-state-graph.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a default fully-qualified app name. Truncated at 63 chars to satisfy
DNS label limits.
*/}}
{{- define "kube-state-graph.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Chart name + version label, e.g. "kube-state-graph-0.1.0".
*/}}
{{- define "kube-state-graph.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels.
*/}}
{{- define "kube-state-graph.labels" -}}
helm.sh/chart: {{ include "kube-state-graph.chart" . }}
{{ include "kube-state-graph.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/*
Selector labels (must be stable across upgrades).
*/}}
{{- define "kube-state-graph.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kube-state-graph.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
ServiceAccount name to use.
*/}}
{{- define "kube-state-graph.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "kube-state-graph.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
API key secret name. Uses the externally-provided secret when set,
otherwise the chart-managed one.
*/}}
{{- define "kube-state-graph.apiKeySecretName" -}}
{{- if .Values.apiKeys.existingSecret -}}
{{- .Values.apiKeys.existingSecret -}}
{{- else -}}
{{- printf "%s-api-keys" (include "kube-state-graph.fullname" .) -}}
{{- end -}}
{{- end -}}

{{/*
True when API-key auth should be wired up (either an existing Secret is
referenced or inline keys are supplied).
*/}}
{{- define "kube-state-graph.apiKeysEnabled" -}}
{{- if or .Values.apiKeys.existingSecret (gt (len .Values.apiKeys.keys) 0) -}}
true
{{- end -}}
{{- end -}}

{{/*
Image reference. Falls back to .Chart.AppVersion when .image.tag is empty.
*/}}
{{- define "kube-state-graph.image" -}}
{{- $tag := .Values.image.tag | default .Chart.AppVersion -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end -}}
