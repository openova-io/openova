"""List the release tags published under each ghcr.io image path (anonymous read)."""
import json
import re
import sys
import urllib.request

STABLE = re.compile(r"^v(\d+)\.(\d+)\.(\d+)$")


def tags(repo):
    tok = json.load(urllib.request.urlopen(f"https://ghcr.io/token?scope=repository:{repo}:pull", timeout=30))["token"]
    url, out = f"https://ghcr.io/v2/{repo}/tags/list?n=1000", []
    while url:
        req = urllib.request.Request(url, headers={"Authorization": f"Bearer {tok}"})
        with urllib.request.urlopen(req, timeout=30) as resp:
            out += json.load(resp).get("tags") or []
            link = resp.headers.get("Link")
        m = re.search(r"<([^>]+)>", link or "")
        url = f"https://ghcr.io{m.group(1)}" if m else None
    return out


for repo in sys.argv[1:]:
    try:
        all_tags = tags(repo)
    except Exception as err:  # noqa: BLE001 - recorded, not fatal
        print(f"ghcr.io/{repo}: could not list tags: {err}")
        continue
    stable = sorted((t for t in all_tags if STABLE.match(t)), key=lambda t: tuple(map(int, STABLE.match(t).groups())))
    print(f"ghcr.io/{repo}: {len(all_tags)} tags, {len(stable)} release tags; newest five: {stable[-5:]};"
          f" has v0.7.5: {'v0.7.5' in all_tags}; has v0.8.8: {'v0.8.8' in all_tags}")
