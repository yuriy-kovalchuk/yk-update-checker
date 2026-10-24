#!/usr/bin/env bash
# Render-time checks of the chart: credentials only in the scan pod, the RBAC split, ssh host keys, and every misconfiguration failing at `helm template`.
set -euo pipefail

chart=${1:-charts/yk-update-checker}
overlay=${2:-examples/credentials.values.yaml}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

# repo dir | Secret | file the scanner reads
expectations=(
  "/etc/update-checker-scanner/secrets/homelab|homelab-github-token|/etc/update-checker-scanner/secrets/homelab/token"
  "/etc/update-checker-scanner/secrets/gitea-apps|gitea-basic-auth|/etc/update-checker-scanner/secrets/gitea-apps/password"
  "/etc/update-checker-scanner/secrets/deploy-key-repo|deploy-key-repo-ssh|/etc/update-checker-scanner/secrets/deploy-key-repo/ssh-privatekey"
)

# A failing positive render must read as a FAIL line, not a bare script crash.
render() { # label -- args of helm template
  local label=$1; shift
  local out
  if ! out=$(helm template "$@" 2>&1); then
    fail "$label: helm template failed: $out"
  fi
  printf '%s' "$out"
}

config=$(render "configmap render" creds "$chart" -f "$overlay" -s templates/configmap.yaml)

# A Secret reference leaking into the config would make the scanner clone unauthenticated.
if grep -q "existingSecret" <<<"$config"; then fail "rendered config still contains existingSecret"; fi

# scanner.trigger no longer exists: the CronJob is the only thing that clones, so the credentials live there and nowhere else.
cronjob=$(render "cronjob render" creds "$chart" -f "$overlay" -s templates/cronjob.yaml)
deployment=$(render "deployment render" creds "$chart" -f "$overlay" -s templates/deployment.yaml)
for entry in "${expectations[@]}"; do
  IFS='|' read -r dir secret file <<<"$entry"
  grep -q "mountPath: \"$dir\"" <<<"$cronjob" || fail "cronjob: nothing mounted at $dir"
  grep -q "secretName: \"$secret\"" <<<"$cronjob" || fail "cronjob: volume for $dir does not reference $secret"
  grep -q "defaultMode: 0440" <<<"$cronjob" || fail "cronjob: credential file is not mode 0440"
  grep -q "$file" <<<"$config" || fail "config: no auth.*_file points at $file"
done
grep -q "readOnly: true" <<<"$cronjob" || fail "cronjob: credential mount is not read-only"

# The dashboard reads the reports and starts scans; it never clones, so it gets no credential, no scanner config and no writable /tmp.
grep -q -- "- dashboard" <<<"$deployment" || fail "deployment does not run the dashboard command"
if grep -q "volumeMounts:\|volumes:" <<<"$deployment"; then
  fail "deployment mounts a volume although it never scans"
fi
if grep -q "repo-cred-" <<<"$deployment"; then
  fail "deployment mounts repository credentials"
fi
if grep -q "name: scanner-tmp" <<<"$deployment"; then
  fail "deployment gets a writable /tmp although it never clones"
fi
if grep -q -- "--server-url\|SCAN_TRIGGER" <<<"$cronjob$deployment"; then
  fail "a pod is still told to POST scan results over HTTP"
fi
grep -q -- "- --store=kubernetes" <<<"$cronjob" || fail "scan Job does not write its reports to the API"
grep -q -- "- --history=" <<<"$cronjob" || fail "scan Job gets no --history"
grep -q "backoffLimit: 0" <<<"$cronjob" || fail "a failed scan Job is retried, which re-clones every repo and repeats each repository's history"
hist=$(helm template h "$chart" --set scanner.history.runs=3 -s templates/cronjob.yaml)
grep -q -- "- --history=3" <<<"$hist" || fail "scanner.history.runs does not reach --history"

# The scan pod writes the reports, so it uses the API — as the scanner ServiceAccount, whose Role grants the two report kinds only.
grep -q "serviceAccountName: creds-yk-update-checker-scanner" <<<"$cronjob" ||
  fail "scan Job does not run as the scanner ServiceAccount"
if grep -q "automountServiceAccountToken: false" <<<"$cronjob"; then
  fail "scan pod has no API token although it must write reports"
fi

# Two identities, two privilege sets: the scanner parses hostile repository content, so its Role stops at the report kinds and its own Lease.
roles=$(render "role render" creds "$chart" -s templates/role.yaml)
[ "$(grep -c "^kind: Role$" <<<"$roles")" -eq 2 ] || fail "expected two Roles, got $(grep -c "^kind: Role$" <<<"$roles")"
scanner_role=$(awk '/^kind: Role$/{n++} n==2' <<<"$roles")
grep -q 'resources: \["repositoryscans", "dependencies"\]' <<<"$scanner_role" ||
  fail "scanner Role does not grant the two report kinds"
if grep -q '"batch"\|"secrets"\|"configmaps"' <<<"$scanner_role" || grep -q 'resourceNames: \["creds-yk-update-checker-leader"\]' <<<"$scanner_role"; then
  fail "scanner Role grants resources beyond the report kinds and the scan Lease"
fi
grep -q 'resourceNames: \["creds-yk-update-checker-leader"\]' <<<"$roles" ||
  fail "dashboard Role cannot update the leader Lease it is named for"
grep -q -- "--lease=creds-yk-update-checker-leader" <<<"$deployment" ||
  fail "deployment --lease is not the Lease the dashboard Role can update"
bindings=$(render "rolebinding render" creds "$chart" -s templates/rolebinding.yaml -s templates/serviceaccount.yaml)
[ "$(grep -c "^kind: RoleBinding$" <<<"$bindings")" -eq 2 ] || fail "expected two RoleBindings"
[ "$(grep -c "^kind: ServiceAccount$" <<<"$bindings")" -eq 2 ] || fail "expected two ServiceAccounts"

# The CRDs are the storage contract between the two halves, so their group must be the one internal/report writes.
crds=$(render "crd render" crds "$chart" --include-crds)
group=$(sed -n 's/.*Group  *= *"\(.*\)".*/\1/p' internal/report/types.go)
for kind in repositoryscans dependencies; do
  grep -q "^  name: $kind\.$group\$" <<<"$crds" || fail "crds/: no CustomResourceDefinition for $kind.$group"
done
grep -q "x-kubernetes-preserve-unknown-fields: true" <<<"$crds" ||
  fail "CRD status is pruned: the API server would drop the report fields"
if grep -q "subresources:" <<<"$crds"; then fail "CRDs have a status subresource; the writer sends the whole object"; fi

# The alert that says a scan broke, since a failed scan leaves the outdated series looking unchanged.
rules=$(render "rule render" h "$chart" -s templates/prometheusrule.yaml --set metrics.prometheusRule.enabled=true)
grep -q "alert: UpdateCheckerRepositoryScanFailed" <<<"$rules" ||
  fail "no alert rule for a repository whose latest scan failed"
rules_off=$(render "rule render off" h "$chart" -s templates/prometheusrule.yaml --set metrics.prometheusRule.enabled=true --set metrics.prometheusRule.scanFailed.enabled=false)
if grep -q "alert: UpdateCheckerRepositoryScanFailed" <<<"$rules_off"; then
  fail "metrics.prometheusRule.scanFailed.enabled=false still renders the rule"
fi

# ssh host keys: the file is rendered, and every ssh repo is pointed at it.
grep -q "known_hosts: |" <<<"$config" || fail "configmap has no known_hosts entry"
grep -q "known_hosts_file: /etc/update-checker/known_hosts" <<<"$config" ||
  fail "ssh repo is not pointed at the rendered known_hosts"

writable_tmp() { # label, rendered yaml — clones go under /tmp and the rootfs is read-only
  local label=$1 doc=$2
  local vol mnt
  # || true: grep -c exits 1 when it finds nothing, and this runs under set -e
  vol=$(grep -A1 -- "- name: scanner-tmp" <<<"$doc" | grep -c "emptyDir: {}" || true)
  mnt=$(grep -A2 -- "- name: scanner-tmp" <<<"$doc" | grep -c "mountPath: /tmp" || true)
  [ "${vol:-0}" -ge 1 ] && [ "${mnt:-0}" -ge 1 ] ||
    fail "$label: no emptyDir /tmp for clones (volumes=$vol mounts=$mnt)"
}
writable_tmp "cronjob" "$cronjob"

# The binary sizes its heap from the cgroup limit; the chart must not set GOMEMLIMIT to the whole container limit again.
if helm template plain "$chart" | grep -q "GOMEMLIMIT"; then fail "chart sets GOMEMLIMIT; the binary derives it (internal/memlimit)"; fi

# A CronJob name over 52 characters is rejected by the API server, and the dashboard must point at the one that rendered.
long=$(helm template homelab-prod-dependency-update-checker-eu "$chart")
cj=$(awk '/^kind: CronJob/{f=1} f&&/^  name:/{print $2; exit}' <<<"$long")
[ "${#cj}" -le 52 ] || fail "CronJob name $cj is ${#cj} characters, the API server allows 52"
grep -q -- "--cronjob=$cj\$" <<<"$long" || fail "dashboard --cronjob does not name the rendered CronJob ($cj)"

# Reports are resources, not process memory, so several dashboard replicas agree.
helm template r "$chart" --set replicaCount=2 >/dev/null 2>&1 ||
  fail "replicaCount=2 must render now that results live in the API"

# A default install must mount nothing at all.
if helm template plain "$chart" | grep -q "repo-cred-"; then fail "default install mounts credentials"; fi
if helm template plain "$chart" | grep -q "existingSecret"; then fail "default install leaks existingSecret into the config"; fi

# Misconfiguration has to fail at render time, not during a scan.
expect_fail() { # label, values, expected message
  printf '%s' "$2" >"$tmp/bad.yaml"
  if msg=$(helm template bad "$chart" -f "$tmp/bad.yaml" 2>&1); then
    fail "$1: render unexpectedly succeeded"
  fi
  grep -q "$3" <<<"$msg" || fail "$1: unexpected message: $msg"
}

expect_fail "existingSecret without auth.type" \
  'scanner:
  config:
    repos:
      - name: a
        repo: https://example.com/a.git
        auth:
          existingSecret: s
' "sets auth.existingSecret but no auth.type"

expect_fail "unknown auth.type" \
  'scanner:
  config:
    repos:
      - name: a
        repo: https://example.com/a.git
        auth:
          type: tokne
          existingSecret: s
' "auth.type \"tokne\" is not one of token, basic, ssh"

expect_fail "existingSecret and an explicit file path" \
  'scanner:
  config:
    repos:
      - name: a
        repo: https://example.com/a.git
        auth:
          type: token
          existingSecret: s
          token_file: /x/y
' "sets both auth.existingSecret and auth.token_file"

expect_fail "repo name unusable as a mount directory" \
  'scanner:
  config:
    repos:
      - name: ".."
        repo: https://example.com/a.git
        auth:
          type: token
          existingSecret: s
' "cannot be used as a credentials directory name"

# A credential value in the config means a credential value in the ConfigMap.
expect_fail "inline auth.token" \
  'scanner:
  config:
    repos:
      - name: a
        repo: https://example.com/a.git
        auth:
          type: token
          token: ghp_secretvalue
' "puts a credential value in the config"

expect_fail "inline auth.password" \
  'scanner:
  config:
    repos:
      - name: a
        repo: https://example.com/a.git
        auth:
          type: basic
          username: u
          password: hunter2
' "puts a credential value in the config"

# The credential header is plaintext anywhere but https.
expect_fail "token auth over http" \
  'scanner:
  config:
    repos:
      - name: a
        repo: http://gitea.internal/a.git
        auth:
          type: token
          existingSecret: s
' "is not https"

# A credentialed URL leaks into the ConfigMap, argv, .git/config and logs.
expect_fail "credentials embedded in the repo URL" \
  'scanner:
  config:
    repos:
      - name: a
        repo: https://x-access-token:ghp_secret@github.com/org/repo.git
' "embeds credentials in the repo URL"

# ssh with no host keys means "believe whoever answers port 22".
expect_fail "ssh deploy key with no known_hosts" \
  'scanner:
  config:
    repos:
      - name: a
        repo: ssh://git@github.com/org/private.git
        auth:
          type: ssh
          existingSecret: s
' "clones over ssh but scanner.ssh.knownHosts is empty"

# The opt-out has to be explicit, but it must stay an opt-out.
printf 'scanner:\n  ssh:\n    strictHostKeyChecking: false\n  config:\n    repos:\n      - name: a\n        repo: ssh://git@github.com/org/private.git\n        auth:\n          type: ssh\n          existingSecret: s\n' >"$tmp/ssh-loose.yaml"
if ! helm template loose "$chart" -f "$tmp/ssh-loose.yaml" >/dev/null 2>&1; then
  fail "strictHostKeyChecking: false should render (with the risk stated)"
fi

# Names that sanitize to one directory collide; the chart uses config.SafeName's rule, so this is the pair config.Load rejects.
expect_fail "two repo names with one credentials directory" \
  'scanner:
  config:
    repos:
      - name: org/app
        repo: https://github.com/a/x.git
        auth: {type: token, existingSecret: ta}
      - name: org-app
        repo: https://github.com/b/x.git
        auth: {type: token, existingSecret: tb}
' "both sanitize to credentials directory"

# config.SafeName's rule: letters of any script kept, a trailing dash kept.
cat >"$tmp/names.yaml" <<'YAML'
scanner:
  config:
    repos:
      - name: café/app
        repo: https://github.com/a/x.git
        auth: {type: token, existingSecret: ta}
      - name: x-
        repo: https://github.com/b/x.git
        auth: {type: token, existingSecret: tb}
      - name: x
        repo: https://github.com/c/x.git
        auth: {type: token, existingSecret: tc}
YAML
names=$(helm template u "$chart" -s templates/cronjob.yaml -f "$tmp/names.yaml")
grep -q 'mountPath: "/etc/update-checker-scanner/secrets/café-app"' <<<"$names" || fail "credential dir does not keep non-ASCII letters like config.SafeName"
grep -q 'mountPath: "/etc/update-checker-scanner/secrets/x-"' <<<"$names" || fail "credential dir trims a trailing dash that config.SafeName keeps"

# Credentials without a type are read and then silently unused.
expect_fail "credentials with no auth.type" \
  'scanner:
  config:
    repos:
      - name: a
        repo: https://example.com/a.git
        auth:
          token_file: /x/y
' "configures credentials but no auth.type"

# Without fsGroup a root-owned 0440 file is unreadable; Helm deep-merges, so the fixture nulls the key.
expect_fail "credentials with no fsGroup on the scan pod" \
  'scanner:
  podSecurityContext:
    fsGroup: null
  config:
    repos:
      - name: a
        repo: https://example.com/a.git
        auth:
          type: token
          existingSecret: s
' "scanner.podSecurityContext .* set securityContext.fsGroup"

# Registry credentials only on the scan pod and in its config; pull secrets on both pods.
ra=(--set scanner.registryAuth.existingSecret=harbor-scan --set 'imagePullSecrets[0].name=harbor-pull')
ra_cron=$(helm template ra "$chart" "${ra[@]}" -s templates/cronjob.yaml)
ra_dep=$(helm template ra "$chart" "${ra[@]}" -s templates/deployment.yaml)
ra_cfg=$(helm template ra "$chart" "${ra[@]}" -s templates/configmap.yaml)
grep -q 'secretName: "harbor-scan"' <<<"$ra_cron" || fail "cronjob: registry credential Secret not mounted"
grep -q 'registry_auth_file: /etc/update-checker-scanner/registry/config.json' <<<"$ra_cfg" || fail "config: registry_auth_file not set"
if grep -q 'harbor-scan' <<<"$ra_dep"; then fail "deployment: the dashboard got registry credentials it never uses"; fi
grep -q 'name: harbor-pull' <<<"$ra_cron" || fail "cronjob: imagePullSecrets missing"
grep -q 'name: harbor-pull' <<<"$ra_dep" || fail "deployment: imagePullSecrets missing"
expect_fail "registryAuth and registry_auth_file together" \
  'scanner:
  registryAuth:
    existingSecret: s
  config:
    registry_auth_file: /x
    repos:
      - name: a
        repo: https://example.com/a.git
' "not both"

# The dashboard always reads the API, so its token must not depend on rbac.create.
if helm template t "$chart" --set rbac.create=false | grep -q "automountServiceAccountToken: false"; then fail "rbac.create=false unmounts the dashboard's API token"; fi
# Bring-your-own ServiceAccounts must name the scanner's too, or the scan Job never starts.
if helm template t "$chart" --set serviceAccount.create=false --set serviceAccount.name=d >/dev/null 2>&1; then fail "serviceAccount.create=false rendered without serviceAccount.scannerName"; fi
helm template t "$chart" --set serviceAccount.create=false --set serviceAccount.name=d --set serviceAccount.scannerName=s | grep -q "serviceAccountName: s$" || fail "the CronJob does not run as serviceAccount.scannerName"

# One scan at a time: the CronJob names its Lease and the scanner Role may hold exactly that one.
rendered=$(helm template t "$chart")
grep -q -- "--lock=t-yk-update-checker-scan" <<<"$rendered" || fail "the scan CronJob passes no --lock Lease"
grep -A1 'resourceNames: \["t-yk-update-checker-scan"\]' <<<"$rendered" | grep -q "get" || fail "the scanner Role cannot hold the scan Lease"

# The same repo, configured the recommended way, must render.
if ! helm template ok "$chart" -f "$overlay" >/dev/null 2>&1; then
  fail "the recommended overlay must render"
fi

echo "OK: credentials and report writes live only with the scan pod, ssh verifies host keys, and bad input fails at render"
