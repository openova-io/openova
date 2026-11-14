#!/usr/bin/env bash
# check-secret-delivery-lint.sh - secret guard G9 (SEC-090).
#
# Delivery lint for every chart template under platform/ and products/,
# vendored charts included: no SecretProviderClass with secretObjects (the
# CSI driver would sync the value into a Secret object), and no
# ExternalSecret on the store openbao-mounted (mounted values are read by the
# pod, never copied into a Secret). This guard has no allow-list: any
# finding fails.
#
# Usage, from any folder:
#   bash scripts/check-secret-delivery-lint.sh                 scan the tree
#   bash scripts/check-secret-delivery-lint.sh --self-test     prove that each rule can fail
#   bash scripts/check-secret-delivery-lint.sh --count <path>  print the number of findings under <path>
#   bash scripts/check-secret-delivery-lint.sh --list <path>   print 'path:count' per file under <path>
#
# The rules are in scripts/secret-guards.py. Exit codes: 0 pass, 1 finding
# (or failed self-test), 2 usage or setup error.
set -euo pipefail
exec python3 "$(dirname "$0")/secret-guards.py" secret-delivery-lint "$@"
