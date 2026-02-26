{{- define "platform-issuers.labels" -}}
{{- range $k, $v := (.Values.labels | default dict) -}}
{{ $k }}: {{ $v | quote }}
{{- end -}}
{{- end -}}

{{- define "platform-issuers.letsencrypt.stagingServer" -}}
{{- .Values.letsencrypt.stagingServer | default "https://acme-staging-v02.api.letsencrypt.org/directory" -}}
{{- end -}}

{{- define "platform-issuers.letsencrypt.prodServer" -}}
{{- .Values.letsencrypt.prodServer | default "https://acme-v02.api.letsencrypt.org/directory" -}}
{{- end -}}

{{- define "platform-issuers.letsencrypt.server" -}}
{{- $env := (.Values.letsencrypt.selectedEnv | default "staging") -}}
{{- if eq $env "prod" -}}
{{ include "platform-issuers.letsencrypt.prodServer" . }}
{{- else -}}
{{ include "platform-issuers.letsencrypt.stagingServer" . }}
{{- end -}}
{{- end -}}
