#!/usr/bin/env python3
"""check-ruleset-drift.py - the live rulesets of the repository equal the files.

.github/rulesets/main.json and .github/rulesets/rc.json are the source of truth for the
rulesets of the branches main and rc/**. Each file is the body of the REST call
POST /repos/{owner}/{repo}/rulesets, with one difference: a bypass actor of type Team names
its team by the slug, "team": "<slug>", instead of "actor_id". A team has no fixed number
before it exists; the slug is resolved when the file is compared or applied.

The one bypass actor of both files is {"actor_type": "DeployKey", "bypass_mode": "always"}.
The API stores it with "actor_id": null: it names no key and covers every write deploy key of
the repository. So the write deploy keys are part of what is compared:
.github/rulesets/deploy-keys.json records them by fingerprint.

Usage:

  python3 scripts/check-ruleset-drift.py                          compare the live rulesets
  python3 scripts/check-ruleset-drift.py --body FILE --team-id N  print the body to apply FILE
  python3 scripts/check-ruleset-drift.py --self-test              proves that each rule can fail

The comparison reads the token in RULESET_READ_TOKEN and the repository in GITHUB_REPOSITORY.
Reading the bypass list and the deploy keys needs the right to read the administration of the
repository.

The rules, exactly.

1. Each file names one ruleset. The live ruleset of the same name must exist.
2. Every key the file states must have the same value live. A key only the live copy has is
   not judged: it is an identifier, a timestamp or a default the file does not state.
3. A list must have as many items live as in the file, and each item of the file must match its
   own live item by rule 2. So a live rule or bypass actor that the file lacks is a difference.
4. A team slug is compared through the team's number, read from the organisation.
5. A token that is absent, a read that fails, or a live copy without bypass actors (the token
   cannot see them) is a failure, never a pass.
6. The write deploy keys of the repository (read_only false, or the field absent) are exactly
   the keys of .github/rulesets/deploy-keys.json, compared by the SHA256 fingerprint of the
   public key. A second write key, another key, or none at all is a difference. A read-only key
   cannot push and is not judged.

Exit code 0: the live rulesets equal the files. 1: a difference, or what the check reads is
missing; one line per finding. 2: wrong usage. Only the Python standard library is used.
"""
import base64
import copy
import hashlib
import json
import os
import re
import sys
import urllib.request

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FILES = (".github/rulesets/main.json", ".github/rulesets/rc.json")
KEYS = ("name", "target", "enforcement", "conditions", "bypass_actors", "rules")
DEPLOY_KEYS = ".github/rulesets/deploy-keys.json"
FINGERPRINT = re.compile(r"^SHA256:[A-Za-z0-9+/]{43}$")


class Unreadable(Exception):
    pass


def load(path):
    try:
        with open(os.path.join(ROOT, path)) as f:
            body = json.load(f)
    except (OSError, ValueError) as e:
        raise Unreadable("%s: %s" % (path, e))
    missing = [k for k in KEYS if k not in body]
    if missing:
        raise Unreadable("%s: missing %s" % (path, ", ".join(missing)))
    for a in body["bypass_actors"]:
        if "bypass_mode" not in a or not ("actor_id" in a or a.get("actor_type") == "DeployKey"
                                          or (a.get("actor_type") == "Team" and a.get("team"))):
            raise Unreadable("%s: bypass actor %s needs bypass_mode and actor_id, or type DeployKey, or type Team and team" % (path, a))
    return body


def fingerprint(public_key):
    """The SHA256 fingerprint of an OpenSSH public key line, as ssh-keygen -l prints it."""
    try:
        blob = base64.b64decode(public_key.split()[1], validate=True)
    except Exception:
        raise Unreadable("a deploy key is not an OpenSSH public key: %r" % public_key[:40])
    return "SHA256:" + base64.b64encode(hashlib.sha256(blob).digest()).decode().rstrip("=")


def load_keys(path):
    """The recorded fingerprints of the write deploy keys."""
    try:
        with open(os.path.join(ROOT, path)) as f:
            keys = json.load(f)["write_keys"]
        prints = [k["fingerprint"] for k in keys]
    except (OSError, ValueError, KeyError, TypeError) as e:
        raise Unreadable("%s: %s" % (path, e))
    bad = [p for p in prints if not isinstance(p, str) or not FINGERPRINT.match(p)]
    if not prints or bad:
        raise Unreadable("%s: needs one or more write_keys, each with a fingerprint SHA256:<43 characters>" % path)
    return set(prints)


def compare_keys(recorded, live_keys):
    """Rule 6: the live write deploy keys equal the recorded fingerprints."""
    live = {}
    for k in live_keys:
        if k.get("read_only") is True:
            continue
        live[fingerprint(str(k.get("key", "")))] = k.get("title", "")
    found = ["%s: write key %s (%s) is not recorded" % (DEPLOY_KEYS, fp, live[fp]) for fp in sorted(set(live) - recorded)]
    found += ["%s: recorded write key %s is not on the repository" % (DEPLOY_KEYS, fp) for fp in sorted(recorded - set(live))]
    return found


def resolve(body, team_id):
    """The API body: every team slug replaced by its number."""
    out = copy.deepcopy(body)
    for a in out["bypass_actors"]:
        if "team" in a:
            a["actor_id"] = team_id(a.pop("team"))
    return out


def diff(want, live, where):
    if isinstance(want, dict):
        if not isinstance(live, dict):
            return ["%s: live is not an object" % where]
        found = []
        for k in sorted(want):
            if k not in live:
                found.append("%s.%s: absent live" % (where, k))
            else:
                found += diff(want[k], live[k], "%s.%s" % (where, k))
        return found
    if isinstance(want, list):
        if not isinstance(live, list) or len(live) != len(want):
            return ["%s: %d items in the file, live %s" % (where, len(want), json.dumps(live, sort_keys=True))]
        free = list(live)
        for i, item in enumerate(want):
            match = next((x for x in free if not diff(item, x, where)), None)
            if match is None:
                return ["%s[%d]: no live item equals %s" % (where, i, json.dumps(item, sort_keys=True))]
            free.remove(match)
        return []
    return [] if want == live else ["%s: file %s, live %s" % (where, json.dumps(want), json.dumps(live))]


def compare(files, live_by_name, team_id):
    found = []
    for path in files:
        body = load(path)
        live = live_by_name(body["name"])
        if live is None:
            found.append("%s: no live ruleset named %s" % (path, body["name"]))
            continue
        if "bypass_actors" not in live:
            found.append("%s: the live ruleset shows no bypass actors; the token cannot read them" % path)
            continue
        found += ["%s: %s" % (path, d) for d in diff(resolve(body, team_id), live, "ruleset")]
    return found


def api(token):
    base = os.environ.get("GITHUB_API_URL", "https://api.github.com")

    def get(path):
        req = urllib.request.Request(base + path, headers={
            "Authorization": "Bearer " + token, "Accept": "application/vnd.github+json",
            "X-GitHub-Api-Version": "2022-11-28"})
        try:
            with urllib.request.urlopen(req, timeout=30) as r:
                return json.load(r)
        except Exception as e:
            raise Unreadable("GET %s: %s" % (path, e))
    return get


def live_readers(get, repo):
    listed = get("/repos/%s/rulesets?includes_parents=false&per_page=100" % repo)

    def by_name(name):
        ids = [r["id"] for r in listed if r.get("name") == name]
        return get("/repos/%s/rulesets/%d" % (repo, ids[0])) if ids else None

    def team_id(slug):
        return get("/orgs/%s/teams/%s" % (repo.split("/")[0], slug))["id"]
    return by_name, team_id


def live_keys(get, repo):
    keys = get("/repos/%s/keys?per_page=100" % repo)
    if not isinstance(keys, list) or len(keys) >= 100:
        raise Unreadable("the deploy keys of %s: not a list, or 100 or more (only one page is read)" % repo)
    return keys


def self_test():
    failed = 0
    cases = []
    for path in FILES:
        body = load(path)
        live = resolve(body, lambda slug: 42)
        for a in live["bypass_actors"]:
            if a.get("actor_type") == "DeployKey":
                a["actor_id"] = None
        live.update({"id": 7, "node_id": "RRS_x", "source": "o/r", "source_type": "Repository",
                     "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z",
                     "_links": {}, "current_user_can_bypass": "always"})
        cases.append(("%s equals its live copy" % path, path, live, 0))

        def mutant(fn, live=live):
            m = copy.deepcopy(live)
            fn(m)
            return m
        cases += [
            ("%s: live enforcement changed" % path, path, mutant(lambda m: m.update(enforcement="evaluate")), 1),
            ("%s: a live rule is missing" % path, path, mutant(lambda m: m["rules"].pop()), 1),
            ("%s: an extra live rule" % path, path, mutant(lambda m: m["rules"].append({"type": "required_signatures"})), 1),
            ("%s: a team bypasses instead of the deploy key" % path, path, mutant(lambda m: m["bypass_actors"][0].update(actor_type="Team", actor_id=43)), 1),
            ("%s: an extra bypass actor" % path, path, mutant(lambda m: m["bypass_actors"].append(
                {"actor_id": 5, "actor_type": "RepositoryRole", "bypass_mode": "always"})), 1),
            ("%s: bypass actors not visible" % path, path, mutant(lambda m: m.pop("bypass_actors")), 1),
            ("%s: another branch" % path, path, mutant(lambda m: m["conditions"]["ref_name"].update(include=["refs/heads/x"])), 1),
            ("%s: no live ruleset" % path, path, None, 1),
        ]
    main_live = [c[2] for c in cases if c[1] == FILES[0] and c[3] == 0][0]

    def rule(m, kind):
        return [r for r in m["rules"] if r["type"] == kind][0]["parameters"]
    cases += [
        ("main: merge method changed", FILES[0], copy.deepcopy(main_live), 1),
        ("main: a required check is gone", FILES[0], copy.deepcopy(main_live), 1),
        ("main: an extra live parameter is not judged", FILES[0], copy.deepcopy(main_live), 0),
    ]
    rule(cases[-3][2], "merge_queue")["merge_method"] = "MERGE"
    rule(cases[-2][2], "required_status_checks")["required_status_checks"].pop(0)
    rule(cases[-1][2], "pull_request")["some_new_default"] = False
    for name, path, live, want in cases:
        got = 1 if compare([path], lambda n, live=live: live, lambda slug: 42) else 0
        print("%s %s (%d, want %d)" % ("ok  " if got == want else "FAIL", name, got, want))
        failed += got != want
    old = os.environ.pop("RULESET_READ_TOKEN", None)
    got = main([])
    print("%s an absent token fails (%d, want 1)" % ("ok  " if got == 1 else "FAIL", got))
    failed += got != 1
    if old is not None:
        os.environ["RULESET_READ_TOKEN"] = old

    def broken(path):
        raise Unreadable("GET %s: HTTP Error 503" % path)
    try:
        live_readers(broken, "o/r")
        got = 0
    except Unreadable:
        got = 1
    print("%s a failed read fails (%d, want 1)" % ("ok  " if got == 1 else "FAIL", got))
    failed += got != 1
    body = resolve({"bypass_actors": [{"actor_type": "Team", "team": "t", "bypass_mode": "always"}]}, lambda slug: 99)
    good = body["bypass_actors"] == [{"actor_type": "Team", "actor_id": 99, "bypass_mode": "always"}]
    print("%s --body puts the team number in place of the slug" % ("ok  " if good else "FAIL"))
    failed += not good
    deploy = [a for f in FILES for a in load(f)["bypass_actors"]]
    good = deploy == [{"actor_type": "DeployKey", "bypass_mode": "always"}] * len(FILES)
    print("%s both files name the deploy key as their one bypass actor" % ("ok  " if good else "FAIL"))
    failed += not good

    # Rule 6. The public key is GitHub's published ed25519 host key, whose fingerprint GitHub publishes.
    pub = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"
    other = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBh2oF0Fq2Qr0UEGqZQ+0cX9MZ4wEbJ0dJZRpbCJbHqT"
    fp = fingerprint(pub)
    good = fp == "SHA256:+DiY3wvvV6TuJJhbpZisF/zLDA0zPMSvHdkr4UvCOqU"
    print("%s the fingerprint equals the one ssh-keygen prints (%s)" % ("ok  " if good else "FAIL", fp))
    failed += not good
    key_cases = [
        ("rule 6: the recorded write key alone passes", [{"key": pub, "read_only": False, "title": "release-writer"}], 0),
        ("rule 6: a read-only key beside it is not judged", [{"key": pub, "read_only": False}, {"key": other, "read_only": True}], 0),
        ("rule 6: a second write key fails", [{"key": pub, "read_only": False}, {"key": other, "read_only": False}], 1),
        ("rule 6: another write key in its place fails", [{"key": other, "read_only": False}], 1),
        ("rule 6: no write key fails", [{"key": pub, "read_only": True}], 1),
        ("rule 6: a key without read_only counts as a write key", [{"key": pub, "read_only": False}, {"key": other}], 1),
    ]
    for name, keys, want in key_cases:
        got = 1 if compare_keys({fp}, keys) else 0
        print("%s %s (%d, want %d)" % ("ok  " if got == want else "FAIL", name, got, want))
        failed += got != want
    try:
        compare_keys({fp}, [{"key": "ssh-ed25519 not-base64!", "read_only": False}])
        got = 0
    except Unreadable:
        got = 1
    print("%s rule 6: a key that cannot be read fails (%d, want 1)" % ("ok  " if got == 1 else "FAIL", got))
    failed += got != 1
    try:
        got = 0 if load_keys(DEPLOY_KEYS) else 1
    except Unreadable as e:
        print("     %s" % e)
        got = 1
    print("%s %s holds valid fingerprints (%d, want 0)" % ("ok  " if got == 0 else "FAIL", DEPLOY_KEYS, got))
    failed += got != 0
    print("self-test: %s" % ("FAIL" if failed else "%d cases passed" % (len(cases) + len(key_cases) + 7)))
    return 1 if failed else 0


def main(argv):
    if argv == ["--self-test"]:
        return self_test()
    if len(argv) == 4 and argv[0] == "--body" and argv[2] == "--team-id" and argv[3].isdigit():
        try:
            print(json.dumps(resolve(load(argv[1]), lambda slug: int(argv[3])), indent=2))
        except Unreadable as e:
            print("FAIL %s" % e)
            return 1
        return 0
    if argv:
        print(__doc__)
        return 2
    token, repo = os.environ.get("RULESET_READ_TOKEN", ""), os.environ.get("GITHUB_REPOSITORY", "")
    if not token or not repo:
        print("FAIL RULESET_READ_TOKEN and GITHUB_REPOSITORY must be set")
        return 1
    try:
        get = api(token)
        found = compare(FILES, *live_readers(get, repo))
        found += compare_keys(load_keys(DEPLOY_KEYS), live_keys(get, repo))
    except Unreadable as e:
        found = ["unreadable: %s" % e]
    for line in found:
        print("FAIL " + line)
    print("ruleset drift: %s" % ("%d finding(s)" % len(found) if found else "none"))
    return 1 if found else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
