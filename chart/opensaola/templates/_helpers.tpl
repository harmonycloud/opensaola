{{/* vim: set filetype=mustache: */}}
{{/*
Expand the name of the chart.
*/}}
{{- define "opensaola.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "opensaola.fullname" -}}
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
Create a DNS-safe name by appending a suffix while keeping the final name
within Kubernetes' 63-character DNS label limit.
*/}}
{{- define "opensaola.suffixedName" -}}
{{- $root := .root -}}
{{- $suffix := .suffix -}}
{{- $base := include "opensaola.fullname" $root -}}
{{- $baseMaxLen := sub 62 (len $suffix) | int -}}
{{- printf "%s-%s" ($base | trunc $baseMaxLen | trimSuffix "-") $suffix | trunc 63 | trimSuffix "-" -}}
{{- end }}

{{/*
Namespace where middleware package Secrets live. Empty value defaults to the
release namespace so a normal `helm upgrade --install --create-namespace`
works out of the box.
*/}}
{{- define "opensaola.dataNamespace" -}}
{{- default .Release.Namespace .Values.config.dataNamespace | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Operator image.
*/}}
{{- define "opensaola.image" -}}
{{- if .Values.image.registry }}{{ .Values.image.registry }}/{{ end }}{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end }}

{{/*
Kubectl image used by the CRD hook job.
*/}}
{{- define "opensaola.kubectlImage" -}}
{{- if .Values.kubectl.image.registry }}{{ .Values.kubectl.image.registry }}/{{ end }}{{ .Values.kubectl.image.repository }}:{{ .Values.kubectl.image.tag }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "opensaola.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "opensaola.labels" -}}
helm.sh/chart: {{ include "opensaola.chart" . }}
{{ include "opensaola.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "opensaola.selectorLabels" -}}
app.kubernetes.io/name: {{ include "opensaola.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "opensaola.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "opensaola.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "opensaola.commonLabels" -}}
app.kubernetes.io/name: {{ include "opensaola.name" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
{{/*app.kubernetes.io/managed-by: {{ .Release.Service }}*/}}
helm.sh/chart: "{{ .Chart.Name }}-{{ .Chart.Version | replace "+" "_" }}"
{{- end -}}
