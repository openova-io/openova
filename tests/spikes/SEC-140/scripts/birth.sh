#!/bin/sh
# One-shot birth of a mounted value (fallback variant of the birth pattern).
# Logs in with the pod's own service-account token (audience openbao), checks the
# metadata path to learn whether the value exists, and creates it with cas=0 when
# it does not. It never reads a value and never overwrites one.
# Inputs (env): NS (pod namespace), KEY (path below mounted/<NS>/), BAO_ADDR, ROLE.
set -u
: "${NS:?}" "${KEY:?}" "${BAO_ADDR:?}"
ROLE=${ROLE:-birth}
jwt=$(cat /var/run/secrets/openbao/token)
if ! tok=$(bao write -field=token auth/kubernetes/login role="$ROLE" jwt="$jwt" 2>/tmp/login.err); then
  echo "RESULT login-failed $(tr '\n' ' ' </tmp/login.err)"
  exit 2
fi
export BAO_TOKEN="$tok"
p="$NS/$KEY"
if out=$(bao read -format=json "mounted/metadata/$p" 2>&1); then
  echo "RESULT exists $p (metadata read; value not read)"
  exit 0
fi
case "$out" in
  *"No value found"*) : ;;
  *) echo "RESULT metadata-check-error $(echo "$out" | tr '\n' ' ')"; exit 3 ;;
esac
pw=$(head -c 96 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | cut -c1-32)
if out=$(printf '{"options":{"cas":0},"data":{"password":"%s"}}' "$pw" | bao write "mounted/data/$p" - 2>&1); then
  echo "RESULT created $p"
  exit 0
fi
echo "RESULT create-failed $(echo "$out" | tr '\n' ' ')"
exit 4
