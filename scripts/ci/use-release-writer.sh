#!/usr/bin/env bash
# use-release-writer.sh - `git push origin` in this checkout pushes with the release-writer deploy
# key; fetches keep the https remote. Run by .github/actions/use-release-writer. Needs
# RELEASE_WRITER_KEY (the private key), RUNNER_TEMP, GITHUB_REPOSITORY. Prints nothing.
set -euo pipefail
: "${RELEASE_WRITER_KEY:?RELEASE_WRITER_KEY is not set}"
: "${RUNNER_TEMP:?RUNNER_TEMP is not set}"
: "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is not set}"
key="$RUNNER_TEMP/release-writer.key"
hosts="$RUNNER_TEMP/release-writer.known_hosts"
umask 077
printf '%s\n' "$RELEASE_WRITER_KEY" > "$key"
chmod 600 "$key"
# GitHub's published ed25519 host key (the field ssh_keys of https://api.github.com/meta).
printf '%s\n' 'github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl' > "$hosts"
git config core.sshCommand "ssh -i '$key' -o IdentitiesOnly=yes -o UserKnownHostsFile='$hosts' -o StrictHostKeyChecking=yes"
git remote set-url --push origin "git@github.com:${GITHUB_REPOSITORY}.git"
