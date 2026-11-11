#!/usr/bin/env bash
# playwright-chromium-install.sh — install Playwright's Chromium for CI without
# depending on the Ubuntu apt mirrors unless the runner actually lacks a library.
#
# Why (PR #7085, 2026-11-11): on GitHub's ubuntu-24.04 runners DNS for the
# Ubuntu mirrors (azure.archive.ubuntu.com, archive.ubuntu.com) failed
# intermittently for hours. `npx playwright install --with-deps chromium` runs
# apt-get, so every Playwright job failed before running a test, or crawled
# through DNS timeouts until its 30-minute job timeout. The browser itself comes
# from Playwright's CDN, and the runner image already carries the libraries
# headless Chromium needs — apt is only a fallback.
#
#   1. download Chromium (Playwright CDN, no apt);
#   2. launch it once — if it starts, done, apt never runs;
#   3. otherwise install the OS packages through apt with retries, time-boxed,
#      pointing apt straight at archive.ubuntu.com after the first failure.
# Runs in the caller's working directory (where @playwright/test is installed).
set -euo pipefail

npx playwright install chromium

if node -e '
  let pw;
  for (const m of ["@playwright/test", "playwright", "playwright-core"]) {
    try { pw = require(m); break; } catch (e) {}
  }
  if (!pw) { console.error("no playwright module resolvable from " + process.cwd()); process.exit(2); }
  pw.chromium.launch()
    .then((b) => b.close())
    .then(() => process.exit(0), (e) => { console.error(String(e.message).split("\n")[0]); process.exit(1); });
'; then
  echo "Chromium launches with the runner image's libraries — no OS packages installed, apt not used."
  exit 0
fi

echo "::warning title=Chromium needs OS packages::installing them through apt (retried, time-boxed)"
sudo tee /etc/apt/apt.conf.d/80-ci-retries >/dev/null <<'APT'
Acquire::Retries "5";
Acquire::http::Timeout "20";
Acquire::https::Timeout "20";
APT
for attempt in 1 2 3; do
  if timeout 480 npx playwright install-deps chromium; then
    exit 0
  fi
  if [ "$attempt" = 1 ]; then
    for f in /etc/apt/sources.list.d/ubuntu.sources /etc/apt/sources.list; do
      if [ -f "$f" ] && grep -q 'mirror+file:' "$f"; then
        sudo sed -i -E 's#mirror\+file:[^ ]*#http://archive.ubuntu.com/ubuntu/#g' "$f"
      fi
    done
  fi
  sleep $((attempt * 15))
done
echo "::error title=Chromium OS packages::apt could not install Playwright's Chromium dependencies after 3 attempts"
exit 1
