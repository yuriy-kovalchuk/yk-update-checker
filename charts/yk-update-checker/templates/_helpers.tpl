{{/* Chart name. */}}
{{- define "yk-update-checker.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/* Fully qualified app name, capped at 63 characters. */}}
{{- define "yk-update-checker.fullname" -}}
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

{{/* Scanner CronJob name, capped at 52 characters (the controller suffixes Job names). */}}
{{- define "yk-update-checker.scannerName" -}}
{{- printf "%s-scanner" (include "yk-update-checker.fullname" . | trunc 44 | trimSuffix "-") -}}
{{- end }}

{{/* The scanner's own identity: it writes the reports, nothing else. */}}
{{- define "yk-update-checker.scannerServiceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- printf "%s-scanner" (include "yk-update-checker.serviceAccountName" .) -}}
{{- else -}}
{{- required "serviceAccount.create=false needs serviceAccount.scannerName: the scan CronJob runs as its own ServiceAccount" .Values.serviceAccount.scannerName -}}
{{- end -}}
{{- end -}}

{{/* Lease the scan holds, so only one scan runs at a time. */}}
{{- define "yk-update-checker.scanLeaseName" -}}
{{- printf "%s-scan" (include "yk-update-checker.fullname" .) -}}
{{- end -}}

{{/* Lease the dashboard replicas elect on; the leader exports the metrics. */}}
{{- define "yk-update-checker.leaseName" -}}
{{- printf "%s-leader" (include "yk-update-checker.fullname" .) -}}
{{- end -}}

{{/* Chart label value. */}}
{{- define "yk-update-checker.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/* Common labels, selector labels included. */}}
{{- define "yk-update-checker.labels" -}}
helm.sh/chart: {{ include "yk-update-checker.chart" . }}
{{ include "yk-update-checker.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/* Selector labels. */}}
{{- define "yk-update-checker.selectorLabels" -}}
app.kubernetes.io/name: {{ include "yk-update-checker.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/* Dashboard ServiceAccount name. */}}
{{- define "yk-update-checker.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "yk-update-checker.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}
