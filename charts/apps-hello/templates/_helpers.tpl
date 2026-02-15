{{- define "apps-hello.name" -}}
{{- default .Chart.Name .Values.nameOverride -}}
{{- end -}}

{{- define "apps-hello.labels" -}}
app.kubernetes.io/name: {{ include "apps-hello.name" . | quote }}
app.kubernetes.io/instance: {{ .Release.Name | quote }}
{{ range $k, $v := (.Values.labels | default dict) }}
{{ $k }}: {{ $v | quote }}
{{ end }}
{{- end -}}
