{{/* vim: set filetype=mustache: */}}
{{/*
Expand the name of the chart.
展开 Chart 名称。
*/}}
{{- define "opensaola.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
生成默认的完整应用名称。
We truncate at 63 chars because some Kubernetes name fields are limited to this by the DNS naming spec.
由于部分 Kubernetes 名称字段受 DNS 命名规范限制，长度会截断到 63 个字符。
If release name contains chart name it will be used as a full name.
如果发布实例名称已经包含 Helm 包名称，则直接使用发布实例名称作为完整名称。
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
Create a DNS-safe name by appending a suffix while keeping the final name within Kubernetes' 63-character DNS label limit.
追加后缀生成符合 DNS 规范的名称，并确保最终名称不超过 Kubernetes DNS 标签的 63 字符限制。
*/}}
{{- define "opensaola.suffixedName" -}}
{{- $root := .root -}}
{{- $suffix := .suffix -}}
{{- $base := include "opensaola.fullname" $root -}}
{{- $baseMaxLen := sub 62 (len $suffix) | int -}}
{{- printf "%s-%s" ($base | trunc $baseMaxLen | trimSuffix "-") $suffix | trunc 63 | trimSuffix "-" -}}
{{- end }}

{{/*
Namespace where middleware package Secrets live. Empty value defaults to the release namespace so a normal `helm upgrade --install --create-namespace` works out of the box.
中间件包 Secret 所在命名空间。为空时默认使用发布实例命名空间，确保普通 `helm upgrade --install --create-namespace` 可以开箱即用。
*/}}
{{- define "opensaola.dataNamespace" -}}
{{- default .Release.Namespace .Values.config.dataNamespace | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Operator image.
控制器镜像。
*/}}
{{- define "opensaola.image" -}}
{{- if .Values.image.registry }}{{ .Values.image.registry }}/{{ end }}{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end }}

{{/*
Kubectl image used by the CRD hook job.
CRD 钩子 Job 使用的 kubectl 镜像。
*/}}
{{- define "opensaola.kubectlImage" -}}
{{- if .Values.kubectl.image.registry }}{{ .Values.kubectl.image.registry }}/{{ end }}{{ .Values.kubectl.image.repository }}:{{ .Values.kubectl.image.tag }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
生成 Helm 包标签使用的 Helm 包名称和版本。
*/}}
{{- define "opensaola.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
通用标签
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
选择器标签
*/}}
{{- define "opensaola.selectorLabels" -}}
app.kubernetes.io/name: {{ include "opensaola.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
生成要使用的 ServiceAccount 名称
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
通用标签
*/}}
{{- define "opensaola.commonLabels" -}}
app.kubernetes.io/name: {{ include "opensaola.name" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
{{/*app.kubernetes.io/managed-by: {{ .Release.Service }}*/}}
helm.sh/chart: "{{ .Chart.Name }}-{{ .Chart.Version | replace "+" "_" }}"
{{- end -}}
