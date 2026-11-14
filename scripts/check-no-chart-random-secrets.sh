#!/usr/bin/env bash
# check-no-chart-random-secrets.sh - secret guard G2 (SEC-090).
#
# No new generated secret in an own chart template: randAlphaNum, randAlpha,
# randNumeric, randAscii, genCA, genPrivateKey, genSelfSignedCert,
# genSignedCert, htpasswd and derivePassword, counted per line in every file
# under <chart>/templates/ of a chart under platform/ or products/ (a chart
# inside a charts/ folder is vendored and not counted). A password a chart
# generates at render time lands in a Secret object.
#
# Allow-list: scripts/secret-guard-baselines/check-no-chart-random-secrets.txt, one
# 'path:count' line per file. A rise or a new path fails; a fall passes and
# prints the line to change or delete. Never raise or add a line.
#
# Usage, from any folder:
#   bash scripts/check-no-chart-random-secrets.sh                 scan the tree
#   bash scripts/check-no-chart-random-secrets.sh --self-test     prove that each rule can fail
#   bash scripts/check-no-chart-random-secrets.sh --count <path>  print the number of findings under <path>
#   bash scripts/check-no-chart-random-secrets.sh --list <path>   print 'path:count' per file under <path>
#
# The rules are in scripts/secret-guards.py. Exit codes: 0 pass, 1 finding
# (or failed self-test), 2 usage or setup error.
set -euo pipefail
exec python3 "$(dirname "$0")/secret-guards.py" no-chart-random-secrets "$@"
