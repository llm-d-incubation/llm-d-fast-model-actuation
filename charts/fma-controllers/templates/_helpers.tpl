{{/* This file has been modified with the assistance of Claude Opus 4.6 */}}

{{/*
Common labels for all FMA controller resources.
*/}}
{{- define "fma-controllers.labels" -}}
app.kubernetes.io/name: fma-controllers
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
