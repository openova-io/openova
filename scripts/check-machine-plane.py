#!/usr/bin/env python3
"""check-machine-plane.py - the machine plane gains no unsafe pattern.

Seven metrics, counted per file (hidden folders skipped). The scope and the rule of each are
the constants and the metric functions below: node-auto-reboot (infra/), moving-image-tag
(infra/providers/*/main.tf), forbidden-tool, unpinned-runner-tool and host-key-check-disabled
(the machine runner and its code), free-text-target and machine-credential-field (schema
properties of the machine kinds, the virtual-machine definitions and their values schema;
{{ ... }} spans are removed from a YAML file before it is read).

The counts are compared with scripts/baselines/machine-plane.json (form
openova.io/guard-baseline/v1: counts per metric per path, and allow entries with metric, path,
count, reason and owner, subtracted first). A path absent from a metric is allowed zero. The
last five metrics are hard zeros: no count and no allow entry. Exit 1 when a count is above or
below its entry, an allow entry is above its count or malformed, or a hard zero has an entry.
A baseline only goes down: --write-baseline refuses to raise an entry or add a path, and keeps
the allow list. Usage: check-machine-plane.py [--write-baseline | --self-test] [--root <dir>].
Exit codes: 0 pass; 1 a rule is broken; 2 the tree, a scanned file or the baseline cannot be
read, so nothing was judged.
"""
import argparse
import glob
import json
import os
import re
import shutil
import sys
import tempfile

ME = "check-machine-plane"
BASELINE = "scripts/baselines/machine-plane.json"
SCHEMA = "openova.io/guard-baseline/v1"
METRICS = ("node-auto-reboot", "moving-image-tag", "forbidden-tool", "unpinned-runner-tool",
           "host-key-check-disabled", "free-text-target", "machine-credential-field")
HARD_ZERO = METRICS[2:]
WHY = {
    "node-auto-reboot": "a node must not reboot itself; node reboots are coordinated",
    "moving-image-tag": "a machine image is named by an exact identifier, not a moving tag",
    "forbidden-tool": "this tool must not enter the machine plane",
    "unpinned-runner-tool": "every runner tool is pinned by exact version, sha256 and licence, every base image by digest",
    "host-key-check-disabled": "host-key and certificate checks are never switched off, and errors are never ignored",
    "free-text-target": "a test or run target is never a free-text field",
    "machine-credential-field": "a machine credential is never a field of a resource",
}
RUNNER = "products/machine-runner"
OPS = "platform/machine-operations"
LOCK = RUNNER + "/tools.lock.yaml"
CONTAINERFILE = RUNNER + "/Containerfile"
CRDS = OPS + "/chart/crds"

REBOOT_RE = re.compile(r'Automatic-Reboot "true"')
TAG_RE = re.compile(r'image\s*=\s*"[a-z]+-[0-9.]+"')
TOOL_RE = re.compile(r"(?<![A-Za-z0-9])(wazuh|nmap|awx|caldera|syft|grype)(?![A-Za-z0-9])", re.I)
PACKER_RE = re.compile(r"(?<![A-Za-z0-9])packer(?![A-Za-z0-9])[^0-9\n]{0,24}?v?([0-9]+)\.([0-9]+)", re.I)
HOSTKEY_RE = re.compile(r"StrictHostKeyChecking\s*[=\s]\s*[\"']?no(?![A-Za-z0-9])"
                        r"|UserKnownHostsFile\s*[=\s]\s*[\"']?/dev/null"
                        r"|validate_certs\s*[:=]\s*[\"']?(false|no)(?![A-Za-z0-9])"
                        r"|InsecureIgnoreHostKey"
                        r"|ignore_errors\s*[:=]\s*[\"']?(true|yes)(?![A-Za-z0-9])", re.I)
FROM_RE = re.compile(r"^\s*FROM\s+(.*)$", re.I)
DIGEST_RE = re.compile(r"@sha256:[0-9a-f]{64}$")
SHA_RE = re.compile(r"[0-9a-f]{64}")
VERSION_RE = re.compile(r"([0-9]+:)?v?[0-9]+(\.[0-9]+)+[0-9A-Za-z.+:~_-]*")
LOOSE_RE = re.compile(r"(^|[.-])[xX*]($|[.-])")
TEMPLATE_RE = re.compile(r"\{\{.*?\}\}")
FREE_TEXT = ("host", "url", "path", "command", "address", "cidr")
CREDENTIAL = ("password", "privatekey", "secretkey", "token", "keypair")
OWNER = re.compile(r"^[A-Z]{3}$")
ALLOW_KEYS = ["count", "metric", "owner", "path", "reason"]

class Unreadable(Exception):
    """The tree, a scanned file or the baseline cannot be read. Nothing is judged: exit 2."""

def walk(root, top):
    """Repository paths of the files under top, hidden folders skipped; [] when top is absent."""
    base = os.path.join(root, top)
    out = []
    for folder, dirs, names in os.walk(base):
        dirs[:] = sorted(d for d in dirs if not d.startswith("."))
        for name in names:
            full = os.path.join(folder, name)
            if os.path.isfile(full):
                out.append(os.path.relpath(full, root).replace(os.sep, "/"))
    return sorted(out)

def pattern(root, pattern_text):
    """Repository paths that match a glob pattern, files only, sorted."""
    found = glob.glob(os.path.join(root, pattern_text))
    return sorted(os.path.relpath(p, root).replace(os.sep, "/") for p in found if os.path.isfile(p))

def lines(root, path):
    with open(os.path.join(root, path), "rb") as f:
        return f.read().decode("utf-8", errors="replace").split("\n")

def grep(root, path, regex):
    """['line N', ...] for the lines of one file that match regex."""
    return ["line %d" % (n + 1) for n, line in enumerate(lines(root, path)) if regex.search(line)]

def yaml_text(root, path):
    """The text of one YAML file with every {{ ... }} span removed; PyYAML is checked here."""
    try:
        import yaml  # noqa: F401
    except ImportError:
        raise Unreadable("PyYAML is needed to read %s" % path)
    return "\n".join(TEMPLATE_RE.sub("", line) for line in lines(root, path))

def load_yaml_nodes(root, path):
    """The YAML node trees of one file."""
    import yaml
    try:
        return list(yaml.compose_all(yaml_text(root, path), Loader=yaml.SafeLoader))
    except yaml.YAMLError as e:
        raise Unreadable("%s is not valid YAML: %s" % (path, str(e).replace("\n", " ")))

def properties_named(root, path, names):
    """['line N: property <name>', ...] for every schema property of one file whose name is in names."""
    docs = load_yaml_nodes(root, path)
    import yaml
    out = []

    def visit(node):
        if isinstance(node, yaml.MappingNode):
            for key, value in node.value:
                if isinstance(key, yaml.ScalarNode) and key.value == "properties" and isinstance(value, yaml.MappingNode):
                    for prop, _ in value.value:
                        if isinstance(prop, yaml.ScalarNode) and prop.value.lower() in names:
                            out.append("line %d: property %s" % (prop.start_mark.line + 1, prop.value))
                visit(value)
        elif isinstance(node, yaml.SequenceNode):
            for item in node.value:
                visit(item)

    for doc in docs:
        visit(doc)
    return out

def version_at_least(text, major, minor):
    m = re.match(r"\s*v?([0-9]+)\.([0-9]+)", str(text))
    return bool(m) and (int(m.group(1)), int(m.group(2))) >= (major, minor)

def lock_entries(root):
    """The tool entries of the runner's tool lock, as (line number, value) pairs; [] when it is absent."""
    if not os.path.isfile(os.path.join(root, LOCK)):
        return []
    docs = load_yaml_nodes(root, LOCK)
    import yaml
    data = yaml.safe_load(yaml_text(root, LOCK))
    if len(docs) != 1 or not isinstance(data, dict) or not isinstance(data.get("tools"), list):
        raise Unreadable("%s must be one YAML document with a list under tools" % LOCK)
    tools_node = [v for k, v in docs[0].value if k.value == "tools"][0]
    return [(item.start_mark.line + 1, entry) for item, entry in zip(tools_node.value, data["tools"])]

# ---- the seven metrics; each returns {repository path: [where, ...]}

def node_auto_reboot(root):
    files = walk(root, "infra")
    if not files:
        raise Unreadable("infra holds no file")
    return {p: grep(root, p, REBOOT_RE) for p in files}

def moving_image_tag(root):
    files = pattern(root, "infra/providers/*/main.tf")
    if not files:
        raise Unreadable("no file matches infra/providers/*/main.tf")
    return {p: grep(root, p, TAG_RE) for p in files}

def forbidden_tool(root):
    out = {}
    for p in walk(root, RUNNER) + walk(root, OPS):
        found = []
        for n, line in enumerate(lines(root, p)):
            hit = bool(TOOL_RE.search(line))
            if not hit and p != LOCK:
                hit = any((int(a), int(b)) >= (1, 10) for a, b in PACKER_RE.findall(line))
            if hit:
                found.append("line %d" % (n + 1))
        out[p] = found
    for at, entry in lock_entries(root):
        if isinstance(entry, dict) and str(entry.get("name", "")).strip().lower() == "packer" \
                and version_at_least(entry.get("version", ""), 1, 10):
            out.setdefault(LOCK, []).append("line %d: packer entry" % at)
    return out

def unpinned_runner_tool(root):
    out = {}
    for at, entry in lock_entries(root):
        e = entry if isinstance(entry, dict) else {}
        version, sha, licence = e.get("version"), e.get("sha256"), e.get("licence")
        exact = isinstance(version, str) and VERSION_RE.fullmatch(version) and not LOOSE_RE.search(version)
        if not (isinstance(sha, str) and SHA_RE.fullmatch(sha)) or not (isinstance(licence, str) and licence.strip()) or not exact:
            out.setdefault(LOCK, []).append("line %d: tool entry" % at)
    if os.path.isfile(os.path.join(root, CONTAINERFILE)):
        stages = {"scratch"}
        for n, line in enumerate(lines(root, CONTAINERFILE)):
            m = FROM_RE.match(line)
            if not m:
                continue
            words = [w for w in m.group(1).split() if not w.startswith("--")]
            image = words[0] if words else ""
            if not (DIGEST_RE.search(image) or image.lower() in stages):
                out.setdefault(CONTAINERFILE, []).append("line %d" % (n + 1))
            if len(words) >= 3 and words[1].lower() == "as":
                stages.add(words[2].lower())
    return out

def host_key_check_disabled(root):
    files = walk(root, RUNNER + "/content") + walk(root, "products/catalyst/bootstrap/api/internal/machines/runner")
    return {p: grep(root, p, HOSTKEY_RE) for p in files}

def free_text_target(root):
    files = pattern(root, CRDS + "/testscopes.yaml") + pattern(root, CRDS + "/machineruns.yaml")
    return {p: properties_named(root, p, FREE_TEXT) for p in files}

def machine_credential_field(root):
    files = (pattern(root, CRDS + "/*.yaml")
             + pattern(root, "platform/crossplane-claims/chart/templates/xrds/virtualmachine*.yaml")
             + pattern(root, "platform/virtual-machine/chart/values.schema.json"))
    return {p: properties_named(root, p, CREDENTIAL) for p in files}

MEASURE = dict(zip(METRICS, (node_auto_reboot, moving_image_tag, forbidden_tool, unpinned_runner_tool,
                                host_key_check_disabled, free_text_target, machine_credential_field)))

def measure(root):
    """metric -> repository path -> findings; files without a finding are left out."""
    return {m: {p: f for p, f in MEASURE[m](root).items() if f} for m in METRICS}

def read_baseline(root):
    """Returns (counts, allow entries that are well formed, failure lines about the baseline)."""
    try:
        with open(os.path.join(root, BASELINE), encoding="utf-8") as f:
            doc = json.load(f)
    except (OSError, ValueError) as e:
        raise Unreadable("%s cannot be read: %s" % (BASELINE, e))
    counts = doc.get("counts") if isinstance(doc, dict) else None
    if not isinstance(doc, dict) or doc.get("schema") != SCHEMA or not isinstance(counts, dict) or sorted(counts) != sorted(METRICS):
        raise Unreadable("%s: need schema %s and counts for exactly the metrics %s" % (BASELINE, SCHEMA, ", ".join(METRICS)))
    for metric in METRICS:
        entries = counts[metric]
        if not isinstance(entries, dict) or not all(type(v) is int and v >= 1 for v in entries.values()):
            raise Unreadable("%s: counts.%s must map a path to a whole number of 1 or more" % (BASELINE, metric))
    allow = doc.get("allow")
    if not isinstance(allow, list):
        raise Unreadable("%s: allow must be a list" % BASELINE)
    fails = ["%s is a hard zero: the baseline may hold no count for it (%s)" % (m, ", ".join(sorted(counts[m])))
             for m in HARD_ZERO if counts[m]]
    good, seen = [], set()
    for i, entry in enumerate(allow):
        where = "allow[%d]" % i
        if not isinstance(entry, dict) or sorted(entry) != ALLOW_KEYS:
            fails.append("%s: an allow entry has exactly the keys %s" % (where, ", ".join(ALLOW_KEYS)))
        elif entry["metric"] not in METRICS:
            fails.append("%s: metric must be one of %s" % (where, ", ".join(METRICS)))
        elif entry["metric"] in HARD_ZERO:
            fails.append("%s: %s is a hard zero: it takes no allow entry" % (where, entry["metric"]))
        elif not isinstance(entry["path"], str) or not entry["path"]:
            fails.append("%s: path must be a repository path" % where)
        elif type(entry["count"]) is not int or entry["count"] < 1:
            fails.append("%s: count must be a whole number of 1 or more" % where)
        elif not isinstance(entry["reason"], str) or len(entry["reason"].strip()) < 10:
            fails.append("%s: reason must be ten characters or more" % where)
        elif not isinstance(entry["owner"], str) or not OWNER.match(entry["owner"]):
            fails.append("%s: owner must be an area prefix of three capital letters" % where)
        elif (entry["metric"], entry["path"]) in seen:
            fails.append("%s: a second allow entry for %s %s; one entry per metric and path" % (where, entry["metric"], entry["path"]))
        else:
            seen.add((entry["metric"], entry["path"]))
            good.append(entry)
    return counts, good, fails

def net(have, allow):
    """(metric -> path -> count after the allow entries, failure lines for allow entries above the count)."""
    out, fails = {}, []
    for metric in METRICS:
        out[metric] = {path: len(found) for path, found in have[metric].items()}
    for entry in allow:
        metric, path = entry["metric"], entry["path"]
        now = out[metric].get(path, 0)
        if entry["count"] > now:
            fails.append("%s: %s has %d, its allow entry says %d; lower the allow entry in the same change" % (metric, path, now, entry["count"]))
        out[metric][path] = max(now - entry["count"], 0)
    for metric in METRICS:
        out[metric] = {path: n for path, n in out[metric].items() if n}
    return out, fails

def compare(have, after, entries):
    """One (rose, line) per path whose count after the allow entries is not exactly its baseline entry."""
    out = []
    for metric in METRICS:
        for path in sorted(set(after[metric]) | set(entries[metric])):
            now, entry = after[metric].get(path, 0), entries[metric].get(path, 0)
            if now > entry:
                at = ", ".join(have[metric].get(path, []))
                out.append((True, "%s: %s has %d, the baseline allows %d (at: %s); %s" % (metric, path, now, entry, at, WHY[metric])))
            elif now < entry:
                out.append((False, "%s: %s has %d, the baseline still says %d; lower the baseline in the same change" % (metric, path, now, entry)))
    return out

def run(root, write=False, say=print):
    """The whole guard. Returns the exit code."""
    try:
        have = measure(root)
        target = os.path.join(root, BASELINE)
        if write:
            kept, good = [], []                       # the allow list is kept as it is; only its good entries count
            if os.path.exists(target):
                old, good, _ = read_baseline(root)
                after, over = net(have, good)
                rises = over + [line for rose, line in compare(have, after, old) if rose]
                if rises:
                    for line in rises:
                        say("%s: FAIL %s" % (ME, line))
                    say("%s: the baseline was not written: a baseline only goes down" % ME)
                    return 1
                with open(target, encoding="utf-8") as f:
                    kept = json.load(f)["allow"]
            after, _ = net(have, good)
            os.makedirs(os.path.dirname(target), exist_ok=True)
            with open(target, "w", encoding="utf-8") as f:
                f.write(json.dumps({"schema": SCHEMA, "counts": after, "allow": kept}, indent=2, sort_keys=True) + "\n")
            say("%s: wrote %s" % (ME, BASELINE))
        entries, allow, fails = read_baseline(root)
    except Unreadable as e:
        say("%s: cannot judge: %s" % (ME, e))
        return 2
    after, over = net(have, allow)
    fails += over + [line for rose, line in compare(have, after, entries)]
    for metric in METRICS:
        say("%s=%d" % (metric, sum(len(found) for found in have[metric].values())))
    for line in fails:
        say("%s: FAIL %s" % (ME, line))
    if fails:
        say("%s: %d failure(s). To lower the baseline after a finding left: python3 scripts/%s.py --write-baseline" % (ME, len(fails), ME))
    return 1 if fails else 0

# ---- self-test: every rule is shown to fail on a tree built here; no repository state is read

DIGEST = "@sha256:" + "a" * 64
SUM = "b" * 64
CP = "infra/providers/_shared/cloudinit-control-plane.tftpl"
WORKER = "infra/providers/hetzner/cloudinit-worker.tftpl"
HETZNER = "infra/providers/hetzner/main.tf"
HUAWEI = "infra/providers/huawei/main.tf"
PLAYBOOK = RUNNER + "/content/playbooks/facts.yml"
SSHGO = "products/catalyst/bootstrap/api/internal/machines/runner/ssh.go"
CHART = OPS + "/chart/values.yaml"
TESTSCOPES = CRDS + "/testscopes.yaml"
MACHINERUNS = CRDS + "/machineruns.yaml"
MACHINES = CRDS + "/machines.yaml"
XRD = "platform/crossplane-claims/chart/templates/xrds/virtualmachine.yaml"
SCHEMAJSON = "platform/virtual-machine/chart/values.schema.json"

def crd(props):
    body = "".join("                %s:\n                  type: string\n" % p for p in props)
    return ("apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\n"
            "metadata:\n  name: x.machines.openova.io\nspec:\n  versions:\n    - name: v1alpha1\n"
            "      schema:\n        openAPIV3Schema:\n          type: object\n          properties:\n"
            "            spec:\n              type: object\n              required: [targets]\n"
            "              properties:\n" + body)

FIXTURE = {
    CP: "write_files:\n  - content: |\n      Unattended-Upgrade::Automatic-Reboot \"true\";\n"
        "      Unattended-Upgrade::Automatic-Reboot-Time \"02:30\";\n",
    WORKER: "write_files:\n  - content: |\n      Unattended-Upgrade::Automatic-Reboot \"false\";\n",
    HETZNER: "resource \"hcloud_server\" \"cp\" {\n  image = \"ubuntu-24.04\"\n}\n"
             "resource \"hcloud_server\" \"w\" {\n  image = data.hcloud_image.w.id\n}\n",
    HUAWEI: "resource \"huaweicloud_compute_instance\" \"cp\" {\n  image_id = var.image_id\n}\n",
    LOCK: "schema: openova.io/machine-runner-tools/v1\ntools:\n"
          "  - name: trivy\n    version: \"0.69.1\"\n    source: https://example.org/trivy.tar.gz\n"
          "    sha256: %s\n    licence: Apache-2.0\n    use: separate-process\n"
          "  - name: openscap\n    version: \"1.4.2\"\n    source: https://example.org/oscap.tar.gz\n"
          "    sha256: %s\n    licence: LGPL-2.1-or-later\n    use: separate-process\n" % (SUM, SUM),
    CONTAINERFILE: "FROM --platform=linux/amd64 golang%s AS build\nRUN go build ./...\n"
                   "FROM debian%s\nCOPY --from=build /out /usr/local/bin/\nFROM build AS test\nFROM scratch\n" % (DIGEST, DIGEST),
    PLAYBOOK: "- hosts: all\n  tasks:\n    - name: facts\n      ansible.builtin.setup:\n      ignore_errors: false\n"
              "    - name: fetch\n      ansible.builtin.get_url:\n        validate_certs: true\n",
    SSHGO: "package runner\n\nfunc cb(k ssh.PublicKey) ssh.HostKeyCallback { return ssh.FixedHostKey(k) }\n",
    CHART: "image: machine-runner\nunmapped: true\nunmap: nmapx\n",
    TESTSCOPES: crd(["targets", "ports"]),
    MACHINERUNS: crd(["machineRef", "task"]),
    MACHINES: crd(["primaryAddress", "address", "osFamily"]),
    XRD: "{{- if .Values.virtualMachine.enabled }}\n" + crd(["flavor", "image"]) + "{{- end }}\n",
    SCHEMAJSON: "{\n  \"type\": \"object\",\n  \"properties\": {\n    \"flavor\": {\"type\": \"string\"}\n  }\n}\n",
    "docs/notes.md": "Unattended-Upgrade::Automatic-Reboot \"true\";\n",
}

def self_test():
    try:
        import yaml  # noqa: F401  the property metrics need it
    except ImportError:
        print("%s: self-test needs PyYAML: python3 -m pip install pyyaml" % ME)
        return 2

    def put(root, path, text):
        os.makedirs(os.path.dirname(os.path.join(root, path)), exist_ok=True)
        with open(os.path.join(root, path), "w", encoding="utf-8") as f:
            f.write(text)

    def add(path, text):
        return lambda root: put(root, path, text)

    def edit(path, old, new):
        def change(root):
            if old not in FIXTURE[path]:
                raise AssertionError("fixture text %r not in %s" % (old, path))
            put(root, path, FIXTURE[path].replace(old, new, 1))
        return change

    def baseline(change):
        def apply(root):
            with open(os.path.join(root, BASELINE), encoding="utf-8") as f:
                doc = json.load(f)
            change(doc)
            put(root, BASELINE, json.dumps(doc))
        return apply

    def both(*changes):
        def apply(root):
            for change in changes:
                change(root)
        return apply

    def entry(**over):
        e = {"metric": "moving-image-tag", "path": HUAWEI, "count": 1, "reason": "pack not yet re-proven", "owner": "CLD"}
        e.update(over)
        return e

    def set_allow(*entries):
        return baseline(lambda doc: doc.__setitem__("allow", list(entries)))

    def lock_entry(name="nuclei", version="\"3.4.2\"", sha=SUM, licence="MIT"):
        text = "  - name: %s\n    source: https://example.org/x.tar.gz\n    use: separate-process\n" % name
        text += "".join("    %s: %s\n" % (k, v) for k, v in (("version", version), ("sha256", sha), ("licence", licence)) if v is not None)
        return edit(LOCK, "  - name: openscap\n", text + "  - name: openscap\n")

    new_tag = edit(HUAWEI, "  image_id = var.image_id\n", "  image_id = var.image_id\n  image = \"ubuntu-22.04\"\n")

    def in_playbook(text):
        return edit(PLAYBOOK, "    - name: fetch\n", "    - name: x\n      %s\n    - name: fetch\n" % text)

    def prop(path, name, before="              properties:\n"):
        return edit(path, before, before + "                %s:\n                  type: string\n" % name)

    # (what the case shows, the change to a tree whose baseline is exact, --write-baseline, exit code, text the output must hold)
    cases = [
        ("an unchanged tree passes; the reboot line is counted", lambda root: None, False, 0, "node-auto-reboot=1"),
        ("the moving image tag is counted", lambda root: None, False, 0, "moving-image-tag=1"),
        ("a clean machine plane counts zero; unmap and nmapx are not the word nmap", lambda root: None, False, 0, "forbidden-tool=0"),
        ("an address property in another kind is not a free-text target", lambda root: None, False, 0, "free-text-target=0"),
        ("a second automatic reboot fails", edit(WORKER, "\"false\"", "\"true\""), False, 1, "FAIL node-auto-reboot: %s has 1, the baseline allows 0 (at: line 3)" % WORKER),
        ("an automatic reboot in a new infra file fails", add("infra/k3s/extra.yaml", "Automatic-Reboot \"true\"\n"), False, 1, "FAIL node-auto-reboot: infra/k3s/extra.yaml has 1"),
        ("a reboot line outside infra is not counted", edit("docs/notes.md", "\n", "\nAutomatic-Reboot \"true\"\n"), False, 0, "node-auto-reboot=1"),
        ("a reboot line in a hidden folder is not counted", add("infra/providers/hetzner/.terraform/x.tftpl", "Automatic-Reboot \"true\"\n"), False, 0, "node-auto-reboot=1"),
        ("a moving image tag in another provider fails", new_tag, False, 1, "FAIL moving-image-tag: %s has 1, the baseline allows 0" % HUAWEI),
        ("an image named by an identifier is not counted", edit(HUAWEI, "  image_id = var.image_id\n", "  image = \"img-0a1b2c3d\"\n"), False, 0, "moving-image-tag=1"),
        ("a moving tag outside main.tf is not counted", add("infra/providers/hetzner/other.tf", "image = \"ubuntu-22.04\"\n"), False, 0, "moving-image-tag=1"),
        ("a forbidden tool in the runner fails", add(RUNNER + "/README.md", "Runs Wazuh agents.\n"), False, 1, "FAIL forbidden-tool: %s/README.md has 1" % RUNNER),
        ("the word nmap in the operations chart fails", edit(CHART, "unmapped: true\n", "unmapped: true\nscanner: nmap\n"), False, 1, "FAIL forbidden-tool: %s has 1" % CHART),
        ("a second matcher in the tool lock fails", lock_entry(name="grype"), False, 1, "FAIL forbidden-tool: %s has 1" % LOCK),
        ("packer 1.10 in the Containerfile fails", edit(CONTAINERFILE, "RUN go build", "RUN unzip packer_1.10.3_linux_amd64.zip\nRUN go build"), False, 1, "FAIL forbidden-tool: %s has 1" % CONTAINERFILE),
        ("packer 1.9 is not counted", edit(CONTAINERFILE, "RUN go build", "RUN unzip packer_1.9.5_linux_amd64.zip\nRUN go build"), False, 0, "forbidden-tool=0"),
        ("a packer entry of 1.11 in the tool lock fails", lock_entry(name="packer", version="\"1.11.2\""), False, 1, "packer entry"),
        ("a packer entry of 1.9 in the tool lock is not counted", lock_entry(name="packer", version="\"1.9.5\""), False, 0, "forbidden-tool=0"),
        ("a tool entry without sha256 fails", lock_entry(sha=None), False, 1, "FAIL unpinned-runner-tool: %s has 1" % LOCK),
        ("a tool entry with a short sha256 fails", lock_entry(sha="abc123"), False, 1, "FAIL unpinned-runner-tool: %s has 1" % LOCK),
        ("a tool entry without a licence fails", lock_entry(licence=None), False, 1, "FAIL unpinned-runner-tool: %s has 1" % LOCK),
        ("a tool entry without a version fails", lock_entry(version=None), False, 1, "FAIL unpinned-runner-tool: %s has 1" % LOCK),
        ("a tool entry at version latest fails", lock_entry(version="latest"), False, 1, "FAIL unpinned-runner-tool: %s has 1" % LOCK),
        ("a tool entry at version 3.4.x fails", lock_entry(version="\"3.4.x\""), False, 1, "FAIL unpinned-runner-tool: %s has 1" % LOCK),
        ("a tool entry with a version range fails", lock_entry(version="\">=3.4.2\""), False, 1, "FAIL unpinned-runner-tool: %s has 1" % LOCK),
        ("an unquoted version read as a number fails", lock_entry(version="3.4"), False, 1, "FAIL unpinned-runner-tool: %s has 1" % LOCK),
        ("a FROM line without a digest fails", edit(CONTAINERFILE, "FROM scratch\n", "FROM scratch\nFROM debian:12-slim\n"), False, 1, "FAIL unpinned-runner-tool: %s has 1, the baseline allows 0 (at: line 7)" % CONTAINERFILE),
        ("a FROM line of an unknown stage fails", edit(CONTAINERFILE, "FROM scratch\n", "FROM scratch\nFROM builder\n"), False, 1, "FAIL unpinned-runner-tool: %s has 1" % CONTAINERFILE),
        ("StrictHostKeyChecking=no fails", in_playbook("ansible.builtin.command: ssh -o StrictHostKeyChecking=no h"), False, 1, "FAIL host-key-check-disabled: %s has 1" % PLAYBOOK),
        ("UserKnownHostsFile=/dev/null fails", in_playbook("ansible.builtin.command: ssh -o UserKnownHostsFile=/dev/null h"), False, 1, "FAIL host-key-check-disabled: %s has 1" % PLAYBOOK),
        ("validate_certs: no fails", in_playbook("validate_certs: no"), False, 1, "FAIL host-key-check-disabled: %s has 1" % PLAYBOOK),
        ("ignore_errors: yes fails", in_playbook("ignore_errors: yes"), False, 1, "FAIL host-key-check-disabled: %s has 1" % PLAYBOOK),
        ("InsecureIgnoreHostKey in the runner code fails", edit(SSHGO, "ssh.FixedHostKey(k)", "ssh.InsecureIgnoreHostKey()"), False, 1, "FAIL host-key-check-disabled: %s has 1" % SSHGO),
        ("a disabled check outside the two folders is not counted", add("products/catalyst/bootstrap/api/internal/other/x.go", "ssh.InsecureIgnoreHostKey()\n"), False, 0, "host-key-check-disabled=0"),
        ("a free-text host in the test scope fails", prop(TESTSCOPES, "host"), False, 1, "FAIL free-text-target: %s has 1, the baseline allows 0 (at: line 16: property host)" % TESTSCOPES),
        ("a free-text command in a run fails, whatever its case", prop(MACHINERUNS, "Command"), False, 1, "FAIL free-text-target: %s has 1" % MACHINERUNS),
        ("a host that is not a schema property is not counted", edit(TESTSCOPES, "required: [targets]", "required: [targets, host]\n              description: \"host: a name\""), False, 0, "free-text-target=0"),
        ("a host key outside a properties mapping is not counted", edit(TESTSCOPES, "  name: x.machines.openova.io\n", "  name: x.machines.openova.io\n  labels:\n    host: a\n"), False, 0, "free-text-target=0"),
        ("a password property in a run fails", prop(MACHINERUNS, "password"), False, 1, "FAIL machine-credential-field: %s has 1" % MACHINERUNS),
        ("a keyPair property in another kind fails", prop(MACHINES, "keyPair"), False, 1, "FAIL machine-credential-field: %s has 1" % MACHINES),
        ("a privateKey property in the virtual machine definition fails", edit(XRD, "{{- end }}", "                privateKey:\n                  type: string\n{{- end }}"), False, 1, "FAIL machine-credential-field: %s has 1" % XRD),
        ("a token property in the values schema fails", edit(SCHEMAJSON, "\"flavor\":", "\"Token\": {\"type\": \"string\"},\n    \"flavor\":"), False, 1, "FAIL machine-credential-field: %s has 1" % SCHEMAJSON),
        ("an allow entry covers a moving tag", both(new_tag, set_allow(entry())), False, 0, "moving-image-tag=2"),
        ("an allow entry without a reason fails", both(new_tag, set_allow(entry(reason="short"))), False, 1, "reason must be ten characters or more"),
        ("an allow entry without an owner fails", both(new_tag, set_allow(entry(owner="cld"))), False, 1, "owner must be an area prefix"),
        ("an allow entry with an unknown metric fails", both(new_tag, set_allow(entry(metric="X"))), False, 1, "metric must be one of"),
        ("an allow entry with a missing key fails", both(new_tag, baseline(lambda doc: doc.__setitem__("allow", [{"metric": "moving-image-tag", "path": HUAWEI, "count": 1, "reason": "pack not yet re-proven"}]))), False, 1, "exactly the keys"),
        ("two allow entries for one path fail", both(new_tag, set_allow(entry(), entry())), False, 1, "a second allow entry"),
        ("an allow entry above the count fails", set_allow(entry(path=HETZNER, count=2)), False, 1, "%s has 1, its allow entry says 2" % HETZNER),
        ("an allow entry for a hard-zero metric fails", set_allow(entry(metric="forbidden-tool", path=CHART)), False, 1, "forbidden-tool is a hard zero"),
        ("a count for a hard-zero metric fails", baseline(lambda doc: doc["counts"]["host-key-check-disabled"].__setitem__(PLAYBOOK, 1)), False, 1, "host-key-check-disabled is a hard zero"),
        ("a reboot line that left without a lower baseline fails", edit(CP, "Automatic-Reboot \"true\"", "Automatic-Reboot \"false\""), False, 1, "FAIL node-auto-reboot: %s has 0, the baseline still says 1" % CP),
        ("--write-baseline lowers the baseline after a finding left", edit(CP, "Automatic-Reboot \"true\"", "Automatic-Reboot \"false\""), True, 0, "node-auto-reboot=0"),
        ("--write-baseline refuses to raise an entry", edit(HETZNER, "  image = data", "  image = \"debian-12\"\n  image = data"), True, 1, "the baseline was not written"),
        ("--write-baseline refuses to add a file", new_tag, True, 1, "the baseline was not written"),
        ("--write-baseline keeps the allow list", both(new_tag, set_allow(entry())), True, 0, "moving-image-tag=2"),
        ("a baseline entry of 0 is not a pass", baseline(lambda doc: doc["counts"]["node-auto-reboot"].__setitem__(WORKER, 0)), False, 2, "cannot judge"),
        ("a baseline without one metric is not a pass", baseline(lambda doc: doc["counts"].pop("free-text-target")), False, 2, "cannot judge"),
        ("a baseline of another schema is not a pass", baseline(lambda doc: doc.__setitem__("schema", "other")), False, 2, "cannot judge"),
        ("a missing baseline is not a pass", lambda root: os.remove(os.path.join(root, BASELINE)), False, 2, "cannot judge"),
        ("a missing infra tree is not a pass", lambda root: shutil.rmtree(os.path.join(root, "infra")), False, 2, "infra holds no file"),
        ("a tree without a provider main.tf is not a pass", lambda root: [os.remove(os.path.join(root, p)) for p in (HETZNER, HUAWEI)], False, 2, "no file matches infra/providers/*/main.tf"),
        ("a scanned schema that is not YAML is not a pass", add(TESTSCOPES, "spec: [unclosed\n"), False, 2, "is not valid YAML"),
        ("a tool lock without a tool list is not a pass", add(LOCK, "schema: openova.io/machine-runner-tools/v1\n"), False, 2, "a list under tools"),
    ]
    failed = 0
    for name, change, write, want, needle in cases:
        root = tempfile.mkdtemp(prefix="machine-plane-")
        got, said = None, []
        try:
            for path, text in FIXTURE.items():
                put(root, path, text)
            quiet = []
            if run(root, write=True, say=quiet.append) != 0:
                raise AssertionError("the fixture baseline was not written: %s" % quiet)
            change(root)
            before = open(os.path.join(root, BASELINE), "rb").read() if os.path.exists(os.path.join(root, BASELINE)) else None
            got = run(root, write=write, say=said.append)
            ok = got == want and any(needle in line for line in said)
            if write and want == 1:                   # a refused write left the baseline as it was: the tree still fails
                ok = ok and open(os.path.join(root, BASELINE), "rb").read() == before and run(root, say=quiet.append) == 1
            if write and want == 0 and name.endswith("keeps the allow list"):
                with open(os.path.join(root, BASELINE), encoding="utf-8") as f:
                    ok = ok and json.load(f)["allow"] == [entry()]
        except AssertionError as e:
            ok, said = False, [str(e)]
        finally:
            shutil.rmtree(root, ignore_errors=True)
        failed += 0 if ok else 1
        print("%s  %s" % ("ok  " if ok else "FAIL", name) + ("" if ok else "\n      wanted exit %d and %r; got exit %s and %r" % (want, needle, got, said)))
    print("%s: self-test %s: %d of %d cases as expected" % (ME, "failed" if failed else "passed", len(cases) - failed, len(cases)))
    return 1 if failed else 0

def main(argv=None):
    parser = argparse.ArgumentParser(description="The machine plane gains no unsafe pattern.")
    parser.add_argument("--root", default=os.path.dirname(os.path.dirname(os.path.abspath(__file__))), help="the checkout to measure")
    parser.add_argument("--write-baseline", action="store_true", help="write %s from the tree; never raises an entry" % BASELINE)
    parser.add_argument("--self-test", action="store_true", help="prove that every rule can fail")
    args = parser.parse_args(argv)
    return self_test() if args.self_test else run(args.root, write=args.write_baseline)

if __name__ == "__main__":
    sys.exit(main())
