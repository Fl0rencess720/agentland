{{/*
Expand the chart name.
*/}}
{{- define "agentland.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "agentland.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := include "agentland.name" . -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Build a resource name with "<fullname>-<component>".
Usage:
  {{ include "agentland.componentName" (dict "root" . "name" "gateway") }}
*/}}
{{- define "agentland.componentName" -}}
{{- $root := .root -}}
{{- $name := .name -}}
{{- printf "%s-%s" (include "agentland.fullname" $root) $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
