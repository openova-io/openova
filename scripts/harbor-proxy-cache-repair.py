#!/usr/bin/env python3
"""Find and repair Harbor proxy-cache artifacts whose index is served but whose children are gone.

The 2026-09-01 blob-store wipe left proxy-cache projects serving multi-arch indexes (HTTP 200) whose
child manifests / config blobs no longer exist. A kubelet resolves the index and then fails on the child
("manifests/sha256:... not found"), while Harbor's own health endpoint stays green — the artifact record
is intact, only its content is missing. That signature cost hw307 its Phase 1 (issue #6803).

The repair is to DELETE the artifact RECORD in the proxy-cache project. Harbor then re-fetches index,
children and blobs from upstream on the next pull. It never touches the blob store, and it never touches a
project that is not a proxy-cache: wiping the blob store is what caused the incident (#6773).

A cold miss looks the same from the registry API and is NOT this class: for an image with no cached
record Harbor answers the index straight from upstream (200) while a child-by-digest 404s until the
background pull lands, and the kubelet's next retry succeeds. The scan therefore starts from Harbor's
artifact list and probes only tags that already have a cached record, so a not-yet-cached image can never
be reported broken. Measured 2026-09-02: `curlimages/curl:8.10.1` has a 38 MB cached record with all four
children 404 (corruption, repaired here), while `alpine/k8s:1.31.4` has no record at all and its amd64
404 is a cold miss.

    export HARBOR_ADMIN_PASSWORD=...            # or --password-file
    python3 scripts/harbor-proxy-cache-repair.py --self-test
    python3 scripts/harbor-proxy-cache-repair.py --scan
    python3 scripts/harbor-proxy-cache-repair.py --scan --only curlimages/curl
    python3 scripts/harbor-proxy-cache-repair.py --repair --only curlimages/curl

Exit codes: 0 nothing broken (or repair succeeded), 1 broken artifacts found in --scan, 2 usage/credential
error, 3 a repair failed. Credentials are never printed.
"""
import argparse
import base64
import json
import os
import ssl
import sys
import urllib.error
import urllib.parse
import urllib.request

ACCEPT_MANIFEST = ", ".join([
    "application/vnd.oci.image.index.v1+json",
    "application/vnd.docker.distribution.manifest.list.v2+json",
    "application/vnd.oci.image.manifest.v1+json",
    "application/vnd.docker.distribution.manifest.v2+json",
])


def is_proxy_cache(project):
    """A project may be repaired only if Harbor itself calls it a proxy cache AND it is named like one.

    Both halves are load-bearing. `registry_id` is the authoritative flag — a hosted project has none, and
    deleting an artifact there destroys the only copy. The name prefix is the second lock so that a
    mis-typed project id can never reach the delete call.
    """
    name = project.get("name") or ""
    registry_id = project.get("registry_id")
    if registry_id in (None, 0):
        return False
    return name.startswith("proxy-")


class Harbor:
    def __init__(self, url, password, timeout=60):
        self.url = url.rstrip("/")
        self._auth = "Basic " + base64.b64encode(("admin:" + password).encode()).decode()
        self.timeout = timeout
        self.ctx = ssl.create_default_context()

    def _open(self, req):
        try:
            resp = urllib.request.urlopen(req, timeout=self.timeout, context=self.ctx)
            return resp.status, resp.read()
        except urllib.error.HTTPError as exc:
            return exc.code, b""
        except Exception:
            return -1, b""

    def api(self, path, method="GET"):
        req = urllib.request.Request(
            self.url + path, method=method,
            headers={"Authorization": self._auth, "Accept": "application/json"})
        status, body = self._open(req)
        if body.strip():
            try:
                return status, json.loads(body)
            except ValueError:
                return status, None
        return status, None

    def pull_token(self, repository):
        scope = "repository:%s:pull" % repository
        status, body = self.api("/service/token?service=harbor-registry&scope=" + urllib.parse.quote(scope))
        return (body or {}).get("token")

    def registry(self, path, token, accept=ACCEPT_MANIFEST):
        req = urllib.request.Request(
            self.url + path, headers={"Authorization": "Bearer " + token, "Accept": accept})
        return self._open(req)

    def proxy_projects(self):
        status, projects = self.api("/api/v2.0/projects?page_size=100")
        if status != 200:
            raise SystemExit("harbor: GET /projects returned %s (credentials or endpoint)" % status)
        return [p for p in (projects or []) if is_proxy_cache(p)]

    def repositories(self, project):
        out, page = [], 1
        while True:
            status, repos = self.api(
                "/api/v2.0/projects/%s/repositories?page_size=100&page=%d" % (project, page))
            if status != 200 or not repos:
                return out
            out.extend(repos)
            if len(repos) < 100:
                return out
            page += 1

    def artifacts(self, project, repository):
        encoded = urllib.parse.quote(repository.split("/", 1)[1], safe="")
        status, arts = self.api(
            "/api/v2.0/projects/%s/repositories/%s/artifacts?page_size=100&with_tag=true"
            % (project, encoded))
        return arts or []

    def delete_artifact(self, project, repository, reference):
        encoded = urllib.parse.quote(repository.split("/", 1)[1], safe="")
        status, _ = self.api(
            "/api/v2.0/projects/%s/repositories/%s/artifacts/%s" % (project, encoded, reference),
            method="DELETE")
        return status


def probe(harbor, repository, reference):
    """Return (index_status, [(platform, child_status, config_blob_status)]) for one tag."""
    token = harbor.pull_token(repository)
    if not token:
        return -1, []
    status, body = harbor.registry("/v2/%s/manifests/%s" % (repository, reference), token)
    if status != 200:
        return status, []
    try:
        manifest = json.loads(body)
    except ValueError:
        return status, []
    children = manifest.get("manifests") or []
    results = []
    if not children:
        config = (manifest.get("config") or {}).get("digest")
        blob = None
        if config:
            blob, _ = harbor.registry("/v2/%s/blobs/%s" % (repository, config), token, "*/*")
        results.append(("single", 200, blob))
        return status, results
    for child in children:
        platform = child.get("platform") or {}
        arch = platform.get("architecture")
        if arch in (None, "unknown"):
            continue
        name = "%s/%s" % (platform.get("os"), arch)
        child_status, child_body = harbor.registry(
            "/v2/%s/manifests/%s" % (repository, child["digest"]), token)
        blob = None
        if child_status == 200:
            try:
                config = (json.loads(child_body).get("config") or {}).get("digest")
            except ValueError:
                config = None
            if config:
                blob, _ = harbor.registry("/v2/%s/blobs/%s" % (repository, config), token, "*/*")
        results.append((name, child_status, blob))
    return status, results


def is_broken(index_status, children):
    if index_status != 200:
        return False  # an index that does not serve is a different failure — not this class
    for _name, child_status, blob_status in children:
        if child_status != 200:
            return True
        if blob_status is not None and blob_status not in (200, 206):
            return True
    return False


def scan(harbor, only, verbose):
    broken, checked = [], 0
    for project in harbor.proxy_projects():
        name = project["name"]
        for repo in harbor.repositories(name):
            repository = repo["name"]
            if only and only not in repository:
                continue
            for artifact in harbor.artifacts(name, repository):
                tags = [t["name"] for t in (artifact.get("tags") or [])]
                if not tags:
                    continue  # untagged children are re-created with their parent
                checked += 1
                index_status, children = probe(harbor, repository, tags[0])
                if is_broken(index_status, children):
                    detail = " ".join(
                        "%s=%s%s" % (n, s, "" if b is None else "/blob=%s" % b) for n, s, b in children)
                    broken.append((name, repository, tags, artifact["digest"], detail))
                    print("BROKEN %s:%s index=%s %s" % (repository, tags[0], index_status, detail))
                elif verbose:
                    print("ok     %s:%s" % (repository, tags[0]))
    print("checked %d tagged artifact(s), %d broken" % (checked, len(broken)))
    return broken


def repair(harbor, broken):
    failures = 0
    for project, repository, tags, digest, _detail in broken:
        status = harbor.delete_artifact(project, repository, digest)
        ok = status in (200, 202, 204, 404)
        print("%s %s:%s delete -> %s" % ("repaired" if ok else "FAILED", repository, tags[0], status))
        if not ok:
            failures += 1
            continue
        index_status, children = probe(harbor, repository, tags[0])
        state = "still broken" if is_broken(index_status, children) else "re-fetched clean"
        print("    re-probe index=%s %s -> %s"
              % (index_status,
                 " ".join("%s=%s" % (n, s) for n, s, _b in children) or "(no children)", state))
        if state == "still broken":
            failures += 1
    return failures


def self_test():
    """Exercise the two predicates that decide whether anything gets deleted. No network."""
    cases_guard = [
        ({"name": "proxy-ghcr", "registry_id": 3}, True, "proxy cache with a registry"),
        ({"name": "proxy-dockerhub", "registry_id": 1}, True, "proxy cache with a registry"),
        ({"name": "library", "registry_id": None}, False, "hosted project — never deletable"),
        ({"name": "iogrid", "registry_id": None}, False, "hosted project that lost images in the wipe"),
        ({"name": "iogrid", "registry_id": 2}, False, "registry set but not named like a proxy"),
        ({"name": "proxy-ghcr", "registry_id": None}, False, "named like a proxy but hosted"),
        ({"name": "proxy-ghcr", "registry_id": 0}, False, "registry_id 0 is not a registry"),
        ({}, False, "empty project object"),
    ]
    cases_broken = [
        (200, [("linux/amd64", 404, None)], True, "missing child manifest — the #6803 signature"),
        (200, [("linux/amd64", 200, 200), ("linux/arm64", 404, None)], True, "one arch missing"),
        (200, [("linux/amd64", 200, 404)], True, "child serves but its config blob is gone"),
        (200, [("linux/amd64", 200, 200), ("linux/arm64", 200, 206)], False, "healthy multi-arch"),
        (200, [("single", 200, 200)], False, "healthy single-manifest image"),
        (404, [], False, "index absent — a cache miss, not this class"),
        (-1, [], False, "transport failure — never treated as broken"),
        (200, [], False, "index with no usable children — nothing measured, nothing deleted"),
    ]
    failures = 0
    for project, want, why in cases_guard:
        got = is_proxy_cache(project)
        if got != want:
            print("FAIL is_proxy_cache(%s) = %s, want %s (%s)" % (project, got, want, why))
            failures += 1
    for index_status, children, want, why in cases_broken:
        got = is_broken(index_status, children)
        if got != want:
            print("FAIL is_broken(%s, %s) = %s, want %s (%s)" % (index_status, children, got, want, why))
            failures += 1
    total = len(cases_guard) + len(cases_broken)
    print("self-test: %d/%d cases pass" % (total - failures, total))
    return 0 if failures == 0 else 3


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--url", default=os.environ.get("HARBOR_URL", "https://harbor.openova.io"))
    parser.add_argument("--password-file", help="file holding the Harbor admin password")
    parser.add_argument("--only", help="restrict to repositories containing this substring")
    parser.add_argument("--scan", action="store_true", help="report broken artifacts, change nothing")
    parser.add_argument("--repair", action="store_true", help="delete the broken artifact records")
    parser.add_argument("--self-test", action="store_true", help="run the guard tests offline")
    parser.add_argument("--verbose", action="store_true", help="also print healthy artifacts")
    args = parser.parse_args()

    if args.self_test:
        return self_test()
    if not (args.scan or args.repair):
        parser.error("choose --scan, --repair or --self-test")

    password = os.environ.get("HARBOR_ADMIN_PASSWORD", "")
    if args.password_file:
        with open(args.password_file) as handle:
            password = handle.read().strip()
    if not password:
        print("no Harbor admin password: set HARBOR_ADMIN_PASSWORD or pass --password-file "
              "(mothership Secret openova-harbor/harbor-core, key HARBOR_ADMIN_PASSWORD)", file=sys.stderr)
        return 2

    harbor = Harbor(args.url, password)
    broken = scan(harbor, args.only, args.verbose)
    if not broken:
        return 0
    if not args.repair:
        print("re-run with --repair to delete these artifact records so the proxy re-fetches them")
        return 1
    return 3 if repair(harbor, broken) else 0


if __name__ == "__main__":
    sys.exit(main())
