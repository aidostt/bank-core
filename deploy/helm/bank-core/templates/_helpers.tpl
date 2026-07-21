{{- define "bank-core.labels" -}}
app.kubernetes.io/part-of: bank-core
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
