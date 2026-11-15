#!/usr/bin/env bash
# check-secret-object-register.sh - secret guards G10 and G11 (SEC-095).
#
# G10: the exception register scripts/secret-guard-baselines/secret-object-register.yaml
# has its schema; an entry of class U or T names a removal package and is not
# past its review date; a literal annotation secrets.openova.io/register-id
# names an entry; per own chart, the secretKeyRef lines and the ExternalSecrets
# without that annotation do not rise above the allow-list
# scripts/secret-guard-baselines/check-secret-object-register.txt; a chart with
# the label network.openova.io/metadata-access has a metadataAccess entry.
# G11: the threat model in docs/SECURITY.md equals
# scripts/secret-guard-baselines/threat-model.md.
#
# Usage, from any folder:
#   bash scripts/check-secret-object-register.sh                  scan the tree
#   bash scripts/check-secret-object-register.sh --self-test      prove that each rule can fail
#   bash scripts/check-secret-object-register.sh --counts         print the allow-list lines of the tree as it is
#   bash scripts/check-secret-object-register.sh --census <file>  the matching rule of gate check SEC-G11
#
# The rules are in scripts/secret-object-register.py. Exit codes: 0 pass,
# 1 finding (or failed self-test), 2 usage or setup error.
set -euo pipefail
exec python3 "$(dirname "$0")/secret-object-register.py" "$@"
