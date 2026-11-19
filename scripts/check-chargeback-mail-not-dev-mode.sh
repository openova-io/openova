#!/usr/bin/env bash
# check-chargeback-mail-not-dev-mode.sh — #6843
#
# chargeback's chart documents its own empty-SMTP behaviour as dev mode: the
# binary LOGS the PIN / invite URL instead of mailing it. Mail.Send returns nil,
# so the API answers 200 and the caller sees success. A customer invite that
# was never delivered is indistinguishable from one that was.
#
# Measured on hw307 before this:
#   {"msg":"mail (dev mode, not sent)","to":"<owner>","subject":"Your sign-in code"}
#
# Sign-in no longer depends on mail (#6841 moved it to Sovereign SSO), but
# customer onboarding does — invite links are the ONLY way to activate a
# customer admin.
#
# This gate fails if the Sovereign slot ships without an SMTP secret, or if the
# writer of that Secret does not give it the shape chargeback reads.
#
# The writer is the setup Job of the Sovereign's own mail server (SOV-322,
# platform/stalwart-sovereign/chart/templates/setup-job.yaml): it writes
# catalyst-system/sovereign-smtp-credentials once the server knows the
# credential. The parent's seed that wrote it before is deleted.
set -euo pipefail

CB="${1:-clusters/_template/bootstrap-kit/13f-bp-chargeback.yaml}"
SEED="${2:-platform/stalwart-sovereign/chart/templates/setup-job.yaml}"
fail=0

[[ -f "$CB" ]] || { echo "FAIL: $CB not found"; exit 1; }
grep -qE '^\s*profile:\s*sovereign\s*$' "$CB" || { echo "FAIL: $CB is not the sovereign slot"; exit 1; }

secret=$(awk '/^\s*smtp:/{f=1;next} f&&/^\s*existingSecret:/{print $2;exit} f&&/^\s*[a-zA-Z]/{f=0}' "$CB" | tr -d '"')
if [[ -z "$secret" ]]; then
  echo "FAIL: $CB leaves smtp.existingSecret unset — chargeback silently discards"
  echo "      every invite link and statement while answering 200 (#6843)."
  fail=1
else
  echo "  ok: sovereign slot points at SMTP secret '$secret'"
fi

# The seeder must actually produce the key shape this chart's envFrom needs,
# and must allow reflection into the consuming namespace. Either missing and
# the wiring above is inert.
if [[ -f "$SEED" ]]; then
  for k in SMTP_HOST SMTP_PORT SMTP_FROM SMTP_USER SMTP_PASS; do
    if ! grep -q "\"$k\":" "$SEED"; then
      echo "FAIL: the writer does not write $k — an envFrom consumer gets no SMTP config."
      fail=1
    fi
  done
  grep -q 'reflection-allowed-namespaces' "$SEED" || {
    echo "FAIL: the written Secret is not reflectable — it can never reach the app's namespace"
    echo "      (Kubernetes forbids a cross-namespace secretKeyRef)."; fail=1; }
  # The default of sovereignSMTPCredentialsMirror.reflectionNamespaces in the template.
  ns=$(grep -oE 'default "[^"]*" \$cfg\.reflectionNamespaces' "$SEED" | head -1 | sed -E 's/^default "([^"]*)".*/\1/' || true)
  if [[ -n "$secret" && "$ns" != *chargeback* ]]; then
    echo "FAIL: reflection namespaces '$ns' do not include chargeback — the Secret"
    echo "      is named by the slot but never arrives."
    fail=1
  else
    echo "  ok: the writer writes the SMTP_* shape and reflects into '$ns'"
  fi
else
  echo "FAIL: the writer of the SMTP secret is not at $SEED — nothing gives chargeback its mail settings."
  fail=1
fi

[[ $fail -eq 0 ]] || { echo "FAILED — chargeback would discard mail while reporting success."; exit 1; }
echo "PASS — chargeback has real SMTP credentials on a Sovereign."
