{{- define "memreload.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "memreload.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name (include "memreload.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "memreload.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
app.kubernetes.io/name: {{ include "memreload.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
{{- end -}}

{{- define "memreload.selectorLabels" -}}
app.kubernetes.io/name: {{ include "memreload.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "memreload.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "memreload.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/* Whether RBAC should be cluster-scoped. */}}
{{- define "memreload.clusterScoped" -}}
{{- if eq .Values.scope.mode "cluster" -}}true{{- else -}}false{{- end -}}
{{- end -}}

{{/* The image reference. */}}
{{- define "memreload.image" -}}
{{- $tag := .Values.image.tag | default .Chart.AppVersion -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end -}}

{{/* Name of the chart-created datadog secret. */}}
{{- define "memreload.datadogSecretName" -}}
{{- printf "%s-datadog" (include "memreload.fullname" .) -}}
{{- end -}}

{{/* Name of the chart-created prometheus secret. */}}
{{- define "memreload.prometheusSecretName" -}}
{{- printf "%s-prometheus" (include "memreload.fullname" .) -}}
{{- end -}}

{{/* Name of the chart-created slack secret. */}}
{{- define "memreload.slackSecretName" -}}
{{- printf "%s-slack" (include "memreload.fullname" .) -}}
{{- end -}}

{{/* Name of the chart-created notification-routes secret. */}}
{{- define "memreload.routesSecretName" -}}
{{- printf "%s-notify-routes" (include "memreload.fullname" .) -}}
{{- end -}}

{{/* Effective routes secret name: existing override, else chart-created. */}}
{{- define "memreload.effectiveRoutesSecret" -}}
{{- .Values.notifications.routesExistingSecret | default (include "memreload.routesSecretName" .) -}}
{{- end -}}

{{/* Whether a routes secret should be mounted (inline routes or an existing secret). */}}
{{- define "memreload.routesEnabled" -}}
{{- if or .Values.notifications.routesExistingSecret (gt (len .Values.notifications.routes) 0) -}}true{{- end -}}
{{- end -}}

{{/* Core permission rules shared by ClusterRole / Role. */}}
{{- define "memreload.rules" -}}
- apiGroups: [""]
  resources: ["pods"]
  verbs: ["get", "list", "watch"]
- apiGroups: ["", "events.k8s.io"]
  resources: ["events"]
  verbs: ["create", "patch"]
- apiGroups: ["apps"]
  resources: ["deployments", "statefulsets", "replicasets"]
  verbs: ["get", "list", "watch", "patch", "update"]
- apiGroups: ["argoproj.io"]
  resources: ["rollouts"]
  verbs: ["get", "list", "watch", "patch", "update"]
- apiGroups: ["metrics.k8s.io"]
  resources: ["pods"]
  verbs: ["get", "list"]
- apiGroups: ["memreload.io"]
  resources: ["memoryleakpolicies"]
  verbs: ["get", "list", "watch", "update", "patch"]
- apiGroups: ["memreload.io"]
  resources: ["memoryleakpolicies/status"]
  verbs: ["get", "update", "patch"]
- apiGroups: ["memreload.io"]
  resources: ["memoryleakpolicies/finalizers"]
  verbs: ["update"]
{{- end -}}
