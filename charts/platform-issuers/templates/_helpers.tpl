{{- define "platform-issuers.labels" -}}
{{- range $k, $v := (.Values.labels | default dict) -}}
{{ $k }}: {{ $v | quote }}
{{- end -}}
{{- end -}}

{{- define "platform-issuers.letsencrypt.server" -}}
{{- $env := (.Values.letsencrypt.selectedEnv | default "staging") -}}
{{- if eq $env "prod" -}}
https://acme-v02.api.letsencrypt.org/directory
{{- else -}}
https://acme-staging-v02.api.letsencrypt.org/directory
{{- end -}}
{{- end -}}
