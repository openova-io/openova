#!/usr/bin/env python3
"""Find Kyverno admission denials in collected text (lab test AIP-010).

  denials.py <file>             -> {"pairs": [[policy, rule]], "resources": [...], "snippets": [...]}
  denials.py --reports <json>   -> failing results of Audit-mode policy reports

Policy and rule names are taken from the live ClusterPolicies, so a pair is
only reported when the text names a real policy followed by one of its rules.
"""
import json
import re
import subprocess
import sys


def policies():
    out = subprocess.run(["kubectl", "get", "clusterpolicy", "-o", "json"], capture_output=True, text=True)
    try:
        items = json.loads(out.stdout)["items"]
    except Exception:  # noqa: BLE001
        return {}
    res = {}
    for p in items:
        rules = [r["name"] for r in p.get("spec", {}).get("rules", [])]
        rules += [r["name"] for r in (p.get("status", {}).get("autogen", {}) or {}).get("rules", []) or []]
        extra = []
        for r in rules:
            extra += ["autogen-" + r, "autogen-cronjob-" + r]
        res[p["metadata"]["name"]] = sorted(set(rules + extra), key=len, reverse=True)
    return res


def parse(text):
    pols = policies()
    pairs = []
    flat = text.replace("\\n", "\n")
    for pol, rules in pols.items():
        for rule in rules:
            if re.search(r"(?<![\w-])" + re.escape(pol) + r":\s*" + re.escape(rule) + r":", flat):
                base = rule
                for pre in ("autogen-cronjob-", "autogen-"):
                    if base.startswith(pre):
                        base = base[len(pre):]
                if [pol, base] not in [[a, b] for a, b in pairs]:
                    pairs.append([pol, base])
    resources = sorted(set(re.findall(r"resource (\S+) was blocked", flat)))
    snippets = []
    for m in re.finditer(r"admission webhook \"[^\"]+\" denied the request", flat):
        s = flat[m.start(): m.start() + 500].replace("\n", " ")
        if s not in snippets:
            snippets.append(s)
        if len(snippets) >= 6:
            break
    return {"pairs": pairs, "resources": resources, "snippets": snippets}


def reports(path):
    try:
        with open(path) as f:
            items = json.load(f).get("items", [])
    except Exception:  # noqa: BLE001
        items = []
    fails = []
    for rep in items:
        scope = rep.get("scope") or {}
        for r in rep.get("results", []) or []:
            if r.get("result") in ("fail", "error"):
                res = (r.get("resources") or [scope])[0] if (r.get("resources") or scope) else {}
                fails.append({"policy": r.get("policy"), "rule": r.get("rule"),
                              "resource": "%s/%s" % (res.get("kind"), res.get("name")),
                              "result": r.get("result")})
    uniq = []
    for x in fails:
        if x not in uniq:
            uniq.append(x)
    return {"fails": uniq}


if __name__ == "__main__":
    if sys.argv[1] == "--reports":
        print(json.dumps(reports(sys.argv[2]), indent=1))
    else:
        with open(sys.argv[1], errors="replace") as f:
            print(json.dumps(parse(f.read()), indent=1))
