#!/usr/bin/env bash
# check-openbao-no-unseal-secret.sh - secret guard G7 (SEC-090).
#
# No new reference to the OpenBao unseal-key Secret or to the root-token
# generation endpoint, in platform/, products/, clusters/, infra/, core/,
# scripts/, tests/ and .github/. The allow-list folder itself is not
# scanned. The two words are spelled out only in scripts/secret-guards.py,
# in parts.
#
# Allow-list: scripts/secret-guard-baselines/check-openbao-no-unseal-secret.txt, one
# 'path:count' line per file. A rise or a new path fails; a fall passes and
# prints the line to change or delete. Never raise or add a line.
#
# Usage, from any folder:
#   bash scripts/check-openbao-no-unseal-secret.sh                 scan the tree
#   bash scripts/check-openbao-no-unseal-secret.sh --self-test     prove that each rule can fail
#   bash scripts/check-openbao-no-unseal-secret.sh --count <path>  print the number of findings under <path>
#   bash scripts/check-openbao-no-unseal-secret.sh --list <path>   print 'path:count' per file under <path>
#
# The rules are in scripts/secret-guards.py. Exit codes: 0 pass, 1 finding
# (or failed self-test), 2 usage or setup error.
set -euo pipefail
exec python3 "$(dirname "$0")/secret-guards.py" openbao-no-unseal-secret "$@"
