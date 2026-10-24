{{/* The container both workloads share. Args: root, name, args, scans (mount config, /tmp and credentials), creds, registry, extraEnv, security, resources, extra. */}}
{{- define "yk-update-checker.container" -}}
{{- $root := .root -}}
- name: {{ .name }}
  image: "{{ $root.Values.image.repository }}:{{ $root.Values.image.tag | default $root.Chart.AppVersion }}"
  imagePullPolicy: {{ $root.Values.image.pullPolicy }}
  args:
    {{- toYaml .args | nindent 4 }}
  {{- with .extra }}
  {{- . | nindent 2 }}
  {{- end }}
  {{- if .scans }}
  volumeMounts:
    - name: config
      mountPath: /etc/update-checker
      readOnly: true
    - name: scanner-tmp
      mountPath: /tmp
    {{- range $c := .creds }}
    - name: repo-cred-{{ index $c "i" | int }}
      mountPath: {{ index $c "dir" | quote }}
      readOnly: true
    {{- end }}
    {{- if .registry }}
    - name: registry-auth
      mountPath: {{ include "yk-update-checker.registryAuthDir" $root | quote }}
      readOnly: true
    {{- end }}
  {{- end }}
  {{- with .extraEnv }}
  env:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  securityContext:
    {{- toYaml .security | nindent 4 }}
  resources:
    {{- toYaml .resources | nindent 4 }}
{{- end -}}

{{/* Volumes for a scanning container: config, /tmp scratch, one Secret volume per credential. */}}
{{- define "yk-update-checker.podVolumes" -}}
- name: config
  configMap:
    name: {{ include "yk-update-checker.fullname" .root }}-config
- name: scanner-tmp
  emptyDir: {}
{{- include "yk-update-checker.credVolumes" .creds }}
{{- with .registry }}
- name: registry-auth
  secret:
    secretName: {{ index . "secret" | quote }}
    defaultMode: 0440
    items:
      - key: {{ index . "key" | quote }}
        path: config.json
{{- end }}
{{- end -}}
