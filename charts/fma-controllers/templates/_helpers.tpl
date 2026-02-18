{{/* This file has been modified with the assistance of Claude Opus 4.6 */}}

{{/*
Service account name used by all FMA controllers.
*/}}
{{- define "fma-controllers.serviceAccountName" -}}
fma-controller
{{- end }}

{{/*
Common labels for all FMA controller resources.
*/}}
{{- define "fma-controllers.labels" -}}
app.kubernetes.io/name: fma-controllers
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Image pull policy for dual-pods-controller.
When global.local is true, use Never; otherwise Always.
*/}}
{{- define "fma-controllers.dpc.imagePullPolicy" -}}
{{- if .Values.global.local -}}
Never
{{- else -}}
Always
{{- end -}}
{{- end }}
