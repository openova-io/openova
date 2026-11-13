#!/usr/bin/env bash
# Keep a kubectl port-forward from 127.0.0.1:<port> to a Service or Pod up.
# Usage: pf.sh <namespace> <target> <port> <log file>
set -euo pipefail
ns=$1; target=$2; port=$3; log=$4
if nc -z 127.0.0.1 "$port" 2>/dev/null; then exit 0; fi
nohup kubectl -n "$ns" port-forward "$target" "$port:$port" >>"$log" 2>&1 &
for _ in $(seq 1 30); do
  if nc -z 127.0.0.1 "$port" 2>/dev/null; then exit 0; fi
  sleep 1
done
echo "port-forward to $ns/$target:$port did not come up" >&2
exit 1
