#!/usr/bin/env bash
# playwright-chromium-install.sh — `npx playwright install --with-deps chromium`
# that survives an unreachable Ubuntu apt mirror.
#
# GitHub's ubuntu-24.04 runners resolve apt through
# mirror+file:/etc/apt/apt-mirrors.txt, whose first entry is
# azure.archive.ubuntu.com. On 2026-11-11 that host stopped resolving on the
# runners for hours ("Temporary failure resolving 'azure.archive.ubuntu.com'")
# while every other host resolved, and apt never fell through to the other
# mirrors — so every Playwright e2e job failed before running a test (PR #7085,
# three attempts). Same runner image that passed on 2026-11-08.
#
# First try the normal install. Only if it fails, point apt straight at
# archive.ubuntu.com and try once more. Runs in the caller's working directory.
set -euo pipefail

if npx playwright install --with-deps chromium; then
  exit 0
fi

echo "::warning title=apt mirror unreachable::playwright --with-deps failed; retrying with apt pointed directly at archive.ubuntu.com"
for f in /etc/apt/sources.list.d/ubuntu.sources /etc/apt/sources.list; do
  if [ -f "$f" ] && grep -q 'mirror+file:' "$f"; then
    sudo sed -i -E 's#mirror\+file:[^ ]*#http://archive.ubuntu.com/ubuntu/#g' "$f"
  fi
done
npx playwright install --with-deps chromium
