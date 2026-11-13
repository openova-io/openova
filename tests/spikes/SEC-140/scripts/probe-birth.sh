#!/bin/sh
# Probes what the create-only birth identity can and cannot do on an existing value.
# Inputs (env): NS, KEY (an existing value below mounted/<NS>/), BAO_ADDR, ROLE.
set -u
: "${NS:?}" "${KEY:?}" "${BAO_ADDR:?}"
ROLE=${ROLE:-birth}
jwt=$(cat /var/run/secrets/openbao/token)
if ! tok=$(bao write -field=token auth/kubernetes/login role="$ROLE" jwt="$jwt" 2>/tmp/login.err); then
  echo "PROBE login FAILED $(tr '\n' ' ' </tmp/login.err)"
  exit 2
fi
export BAO_TOKEN="$tok"
p="$NS/$KEY"
try() {
  label=$1; shift
  if out=$("$@" 2>&1); then
    echo "PROBE $label allowed"
  else
    why=$(echo "$out" | tr '\n' ' ' | grep -o -E 'permission denied|check-and-set parameter did not match[^*]*|No value found|Code: [0-9]+' | head -1)
    echo "PROBE $label refused (${why:-unknown})"
  fi
}
write_json() { printf '%s' "$2" | bao write "$1" -; }
try read-value         bao read -format=json "mounted/data/$p"
try overwrite          write_json "mounted/data/$p" '{"data":{"password":"probe"}}'
try overwrite-cas0     write_json "mounted/data/$p" '{"options":{"cas":0},"data":{"password":"probe"}}'
try read-metadata      bao read -format=json "mounted/metadata/$p"
try delete-metadata    bao delete "mounted/metadata/$p"
try create-other-ns    write_json "mounted/data/some-other-ns/shared/probe/e1" '{"data":{"password":"probe"}}'
try create-new-own-ns  write_json "mounted/data/$NS/shared/probe-new/e1" '{"options":{"cas":0},"data":{"password":"probe"}}'
exit 0
