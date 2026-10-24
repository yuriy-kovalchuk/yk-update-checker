{{/* Per-repo credentials: the user's Secret, one key mounted read-only (0440 + fsGroup) into the scan pod only, named in the ConfigMap as a *_file path. */}}

{{/* Mount root. Kept out of /etc/update-checker, which holds config.yaml. */}}
{{- define "yk-update-checker.credRoot" -}}/etc/update-checker-scanner/secrets{{- end -}}

{{/* A repo's credential directory: config.SafeName's rule, so config.Load and the chart reject the same name collisions. */}}
{{- define "yk-update-checker.credDir" -}}
{{- $safe := regexReplaceAll "[^\\p{L}\\p{Nd}._-]" (.|toString) "-" -}}
{{- if or (empty $safe) (hasPrefix "." $safe) -}}
{{- fail (printf "repo name %q cannot be used as a credentials directory name" .) -}}
{{- end -}}
{{- printf "%s/%s" (include "yk-update-checker.credRoot" .) $safe -}}
{{- end -}}

{{/* The credential plan as JSON (index, Secret, key, config key, path); key defaults: token / password / id_ed25519. */}}
{{- define "yk-update-checker.repoCreds" -}}
{{- $types := dict "token" "token_file" "basic" "password_file" "ssh" "ssh_key_path" -}}
{{- $defaults := dict "token" "token" "basic" "password" "ssh" "id_ed25519" -}}
{{- $out := list -}}
{{- range $i, $r := (.Values.scanner.config).repos -}}
{{- $a := $r.auth -}}
{{- if and $a $a.existingSecret -}}
{{- if not $a.type -}}
{{- fail (printf "repo %q sets auth.existingSecret but no auth.type (token | basic | ssh)" $r.name) -}}
{{- end -}}
{{- if not (hasKey $types $a.type) -}}
{{- fail (printf "repo %q auth.type %q is not one of token, basic, ssh" $r.name $a.type) -}}
{{- end -}}
{{- $configKey := index $types $a.type -}}
{{- if index $a $configKey -}}
{{- fail (printf "repo %q sets both auth.existingSecret and auth.%s: mount a Secret or point at a file you mount yourself, not both" $r.name $configKey) -}}
{{- end -}}
{{- $key := $a.existingSecretKey | default (index $defaults $a.type) -}}
{{- $dir := include "yk-update-checker.credDir" $r.name -}}
{{- $out = append $out (dict "i" $i "name" $r.name "secret" $a.existingSecret "key" $key "dir" $dir "configKey" $configKey "path" (printf "%s/%s" $dir $key)) -}}
{{- end -}}
{{- end -}}
{{- toJson $out -}}
{{- end -}}

{{/* One Secret volume per credential; 0440 because Secret files are root-owned and fsGroup only sets the group. */}}
{{- define "yk-update-checker.credVolumes" -}}
{{- range $c := . }}
- name: repo-cred-{{ index $c "i" | int }}
  secret:
    secretName: {{ index $c "secret" | quote }}
    defaultMode: 0440
    items:
      - key: {{ index $c "key" | quote }}
        path: {{ index $c "key" | quote }}
{{- end }}
{{- end -}}

{{/* scanner.config with every auth.existingSecret rewritten into the *_file path the scanner reads. */}}
{{/* A 0440 credential is readable by the non-root pod only through fsGroup; refuse to render without one. */}}
{{- define "yk-update-checker.requireCredFsGroup" -}}
{{- if and (.fsGroup | empty) .creds -}}
{{- fail (printf "%s: repo credentials are mounted 0440 (root:fsGroup), so set securityContext.fsGroup — without it the non-root pod user cannot read them" .who) -}}
{{- end -}}
{{- end -}}

{{/* Strict host key checking (default): an ssh repo needs scanner.ssh.knownHosts; a short-lived pod cannot trust on first use. */}}
{{- define "yk-update-checker.sshStrict" -}}
{{- if eq (dig "ssh" "strictHostKeyChecking" true (.Values.scanner | default dict)) false -}}false{{- else -}}true{{- end -}}
{{- end -}}

{{- define "yk-update-checker.knownHostsPath" -}}/etc/update-checker/known_hosts{{- end -}}

{{- define "yk-update-checker.hasKnownHosts" -}}
{{- if dig "ssh" "knownHosts" "" (.Values.scanner | default dict) -}}true{{- end -}}
{{- end -}}

{{/* Refuse at render: inline credentials, token/basic over non-https, credentials in the URL, ssh without host keys, colliding mount dirs. */}}
{{- define "yk-update-checker.rejectUnsafeRepos" -}}
{{- $strict := eq (include "yk-update-checker.sshStrict" .) "true" -}}
{{- $knownHosts := eq (include "yk-update-checker.hasKnownHosts" .) "true" -}}
{{- range $r := (.Values.scanner.config).repos -}}
{{- $a := $r.auth -}}
{{- if regexMatch "://[^/@]*:[^/@]*@" ($r.repo | toString) -}}
{{- fail (printf "repo %q embeds credentials in the repo URL; that puts them in the ConfigMap, the git argv, the clone's .git/config and the scan log. Use auth.existingSecret instead" $r.name) -}}
{{- end -}}
{{- if $a -}}
{{- if or $a.token $a.password -}}
{{- fail (printf "repo %q puts a credential value in the config: it would be written to the ConfigMap, readable by anyone with get configmaps. Use auth.existingSecret (the chart mounts it) or auth.token_file / auth.password_file pointing at a file you mount yourself" $r.name) -}}
{{- end -}}
{{- if hasKey (dict "token" 1 "basic" 1) ($a.type | toString) -}}
{{- if not (hasPrefix "https://" ($r.repo | toString)) -}}
{{- fail (printf "repo %q uses auth.type %s, which sends an Authorization header, but its URL (%s) is not https: the credential would leave the cluster in plaintext" $r.name $a.type $r.repo) -}}
{{- end -}}
{{- end -}}
{{- if and (eq ($a.type | toString) "ssh") (or $a.ssh_key_path $a.existingSecret) $strict (not $knownHosts) -}}
{{- fail (printf "repo %q clones over ssh but scanner.ssh.knownHosts is empty: with a read-only rootfs and a short-lived pod ssh accepts any host key. Supply the host keys, or set scanner.ssh.strictHostKeyChecking=false to accept that knowingly" $r.name) -}}
{{- end -}}
{{- if and (not $a.type) (or $a.token_file $a.password_file $a.ssh_key_path $a.username $a.existingSecret) -}}
{{- fail (printf "repo %q configures credentials but no auth.type (token | basic | ssh); the credential would be read and then never used" $r.name) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- $seen := dict -}}
{{- range $c := mustFromJson (include "yk-update-checker.repoCreds" .) -}}
{{- $dir := index $c "dir" -}}
{{- if hasKey $seen $dir -}}
{{- fail (printf "repos %q and %q both sanitize to credentials directory %s: the pod would get two mounts at the same path and the API server would reject it. Rename one repo" (index $seen $dir) (index $c "name") $dir) -}}
{{- end -}}
{{- $_ := set $seen $dir (index $c "name") -}}
{{- end -}}
{{- end -}}

{{/* Registry credentials: the user's dockerconfigjson Secret, mounted into the scan pod as registry_auth_file; {} when unset. */}}
{{- define "yk-update-checker.registryAuth" -}}
{{- $ra := (.Values.scanner.registryAuth | default dict) -}}
{{- if $ra.existingSecret -}}
{{- if (.Values.scanner.config).registry_auth_file -}}
{{- fail "set scanner.registryAuth.existingSecret or scanner.config.registry_auth_file, not both" -}}
{{- end -}}
{{- toJson (dict "secret" $ra.existingSecret "key" ($ra.existingSecretKey | default ".dockerconfigjson")) -}}
{{- else -}}
{{- toJson dict -}}
{{- end -}}
{{- end -}}

{{- define "yk-update-checker.registryAuthDir" -}}/etc/update-checker-scanner/registry{{- end -}}

{{- define "yk-update-checker.scannerConfig" -}}
{{- $cfg := deepCopy .Values.scanner.config -}}
{{- if (mustFromJson (include "yk-update-checker.registryAuth" .)) -}}
{{- $_ := set $cfg "registry_auth_file" (printf "%s/config.json" (include "yk-update-checker.registryAuthDir" .)) -}}
{{- end -}}
{{- $knownHosts := eq (include "yk-update-checker.hasKnownHosts" .) "true" -}}
{{- $repos := mustFromJson (include "yk-update-checker.repoCreds" .) -}}
{{- range $c := $repos -}}
{{- $r := index $cfg.repos (index $c "i" | int) -}}
{{- $_ := unset $r.auth "existingSecret" -}}
{{- $_ := unset $r.auth "existingSecretKey" -}}
{{- $_ := set $r.auth (index $c "configKey") (index $c "path") -}}
{{- end -}}
{{- if $knownHosts -}}
{{- range $i, $r := $cfg.repos -}}
{{- if and $r.auth (eq ($r.auth.type | toString) "ssh") (not $r.auth.known_hosts_file) -}}
{{- $_ := set $r.auth "known_hosts_file" (include "yk-update-checker.knownHostsPath" .) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- toYaml $cfg -}}
{{- end -}}
