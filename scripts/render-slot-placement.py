#!/usr/bin/env python3
"""render-slot-placement.py — placement is DATA, never code (#3373).

THE single placement source of truth for bootstrap-kit slots is
clusters/_template/bootstrap-kit/placement.yaml. This tool makes the
slot files a pure function of that data:

  check   verify every slot's placement-owned HelmRelease fields match
          placement.yaml (CI drift gate — a hand-edited placement field
          fails the build; #3373 DoD-6 "zero hand-coded placement").
  fix     rewrite the placement-owned fields in the slot files from
          placement.yaml (surgical line edits; comments preserved).

Placement-owned fields per slot (everything else in a slot is NOT
owned by this tool):

  * HelmRelease metadata.namespace
      host placement     -> flux-system
      vcluster placement -> the vCluster's host namespace (where the
                            exported vc-<tier> kubeconfig Secret lives;
                            Flux v2 SecretReference contract)
  * metadata.labels."catalyst.openova.io/vcluster" (vcluster slots)
  * spec.targetNamespace   (the app's own / inner namespace)
  * spec.storageNamespace  (vcluster slots; hw92 lesson — unpinned it
                            defaults to the HR's own ns which
                            createNamespace never creates inside the
                            vCluster)
  * spec.kubeConfig.secretRef {name: vc-<tier>, key: config} — the ONE
    generic render mechanism (G92.1); absent on host placements
  * spec.dependsOn:
      - the bp-<tier>-vcluster runtime edge (vcluster slots)
      - explicit `namespace:` on EVERY edge of a vcluster slot (a bare
        name resolves in the HR's OWN namespace -> dangling dep, the
        #3191 wedge shape)
      - dependents of a re-homed HR carry its new namespace
  * spec.install.createNamespace (vcluster slots)
  * declared placement values (fix (a) backendRef aliases)

Exit codes: 0 ok / 1 drift (check) or fix-failure / 2 usage.
"""

from __future__ import annotations

import os
import re
import sys

import yaml

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
KIT = os.path.join(REPO, "clusters", "_template", "bootstrap-kit")
PLACEMENT = os.path.join(KIT, "placement.yaml")

GENERATED_MARK = "# placement-generated (#3373) — source: placement.yaml; edit THAT file + run scripts/render-slot-placement.py fix"


def load_placement():
    with open(PLACEMENT) as f:
        data = yaml.safe_load(f)
    if not data or "slots" not in data or "vclusters" not in data:
        sys.exit("placement.yaml malformed: needs vclusters: + slots:")
    by_hr = {s["hr"]: s for s in data["slots"]}
    return data, by_hr


def hr_namespace_for(slot, vclusters):
    """The namespace the slot's HelmRelease CR itself lives in."""
    vc = slot.get("vcluster", "host")
    if vc == "host":
        return "flux-system"
    return vclusters[vc]["hostNamespace"]


def parse_slot_docs(path):
    with open(path) as f:
        return [d for d in yaml.safe_load_all(f) if d]


def find_hr_doc(docs, hr_name):
    for d in docs:
        if d.get("kind") == "HelmRelease" and d.get("metadata", {}).get("name") == hr_name:
            return d
    return None


def get_dotted(values, dotted):
    cur = values
    for seg in dotted.split("."):
        if not isinstance(cur, dict) or seg not in cur:
            return None
        cur = cur[seg]
    return cur


def check(data, by_hr, verbose=True):
    vclusters = data["vclusters"]
    errors = []

    for slot in data["slots"]:
        fpath = os.path.join(KIT, slot["file"])
        if not os.path.exists(fpath):
            errors.append(f"{slot['file']}: file missing but declared in placement.yaml")
            continue
        docs = parse_slot_docs(fpath)
        hr = find_hr_doc(docs, slot["hr"])
        if hr is None:
            errors.append(f"{slot['file']}: HelmRelease {slot['hr']} not found")
            continue

        vc = slot.get("vcluster", "host")
        spec = hr.get("spec", {})
        md = hr.get("metadata", {})
        want_ns = hr_namespace_for(slot, vclusters)

        if md.get("namespace") != want_ns:
            errors.append(f"{slot['file']}: HR namespace {md.get('namespace')!r} != placement {want_ns!r}")
        if spec.get("targetNamespace") != slot["namespace"]:
            errors.append(f"{slot['file']}: targetNamespace {spec.get('targetNamespace')!r} != placement {slot['namespace']!r}")

        kc = spec.get("kubeConfig", {}).get("secretRef", {})
        deps = spec.get("dependsOn") or []
        dep_names = {d.get("name") for d in deps}

        if vc == "host":
            if kc:
                errors.append(f"{slot['file']}: host placement must NOT carry spec.kubeConfig (has {kc.get('name')!r})")
        else:
            vci = vclusters[vc]
            if kc.get("name") != vci["kubeconfigSecret"] or kc.get("key") != "config":
                errors.append(
                    f"{slot['file']}: kubeConfig.secretRef must be "
                    f"{{name: {vci['kubeconfigSecret']}, key: config}}, got {kc!r}")
            if spec.get("storageNamespace") != slot["namespace"]:
                errors.append(f"{slot['file']}: storageNamespace {spec.get('storageNamespace')!r} != "
                              f"{slot['namespace']!r} (hw92 lesson — must pin to the inner namespace)")
            lbl = (md.get("labels") or {}).get("catalyst.openova.io/vcluster")
            if lbl != vc:
                errors.append(f"{slot['file']}: label catalyst.openova.io/vcluster {lbl!r} != {vc!r}")
            if vci["runtimeHR"] not in dep_names:
                errors.append(f"{slot['file']}: dependsOn missing the {vci['runtimeHR']} runtime edge "
                              f"(the vc-{vc} Secret doesn't exist until it is Ready)")
            if not (spec.get("install") or {}).get("createNamespace"):
                errors.append(f"{slot['file']}: install.createNamespace must be true "
                              f"(provisions the inner {slot['namespace']!r} namespace inside the vCluster)")
            # every edge of an out-of-flux-system HR must be explicit-ns
            for d in deps:
                if not d.get("namespace"):
                    errors.append(f"{slot['file']}: dependsOn {d.get('name')!r} lacks explicit namespace "
                                  f"(bare names resolve in the HR's OWN ns {want_ns!r} -> dangling, #3191 shape)")

        # declared placement values stamped?
        for dotted, want in (slot.get("values") or {}).items():
            got = get_dotted(spec.get("values") or {}, dotted)
            if got != want:
                errors.append(f"{slot['file']}: values.{dotted} = {got!r}, placement declares {want!r}")

        # dependents: every edge pointing at a placement-managed HR must
        # carry that HR's CURRENT namespace (or omit it only when both
        # live in flux-system).
        for d in deps:
            tgt = by_hr.get(d.get("name"))
            if tgt is None:
                continue
            tgt_ns = hr_namespace_for(tgt, vclusters)
            edge_ns = d.get("namespace") or want_ns  # bare = own ns
            if edge_ns != tgt_ns:
                errors.append(f"{slot['file']}: dependsOn {d['name']!r} resolves to ns {edge_ns!r} "
                              f"but that HR lives in {tgt_ns!r} (placement.yaml)")

    # every HR-bearing slot file must be declared in placement.yaml
    declared_files = {s["file"] for s in data["slots"]}
    for fn in sorted(os.listdir(KIT)):
        if not re.match(r"^[0-9].*\.ya?ml$", fn):
            continue
        docs = parse_slot_docs(os.path.join(KIT, fn))
        if any(d.get("kind") == "HelmRelease" for d in docs) and fn not in declared_files:
            errors.append(f"{fn}: HR-bearing slot NOT declared in placement.yaml — placement is data (#3373); add a row")

    # host-minimum discipline: target=host requires a justification
    for slot in data["slots"]:
        if slot.get("target", "host") == "host" and not slot.get("justification"):
            errors.append(f"placement.yaml: {slot['hr']} target=host without justification "
                          f"(founder §4: 'only the minimums could stay there')")

    if errors:
        print("PLACEMENT DRIFT — slots are not a function of placement.yaml:", file=sys.stderr)
        for e in errors:
            print(f"  ✗ {e}", file=sys.stderr)
        return False
    if verbose:
        n_vc = sum(1 for s in data["slots"] if s.get("vcluster", "host") != "host")
        gap = [s["hr"] for s in data["slots"] if s.get("vcluster", "host") != s.get("target", "host")]
        print(f"placement check PASSED — {len(data['slots'])} slots, {n_vc} in vClusters, "
              f"{len(gap)} staged for promotion (target != active): {', '.join(gap) if gap else '-'}")
    return True


# ── fix mode: surgical line edits ───────────────────────────────────

def hr_doc_range(lines, hr_name):
    """Return (start, end) line indexes of the HelmRelease doc named hr_name."""
    starts = [i for i, l in enumerate(lines) if l.strip() == "---"] + [len(lines)]
    prev = 0
    for nxt in starts:
        chunk = lines[prev:nxt]
        text = "\n".join(chunk)
        if "kind: HelmRelease" in text and re.search(rf"^\s*name:\s*{re.escape(hr_name)}\s*$", text, re.M):
            return prev, nxt
        prev = nxt + 1 if nxt < len(lines) else nxt
    return None, None


def set_line_value(lines, start, end, pattern, new_line):
    for i in range(start, end):
        if re.match(pattern, lines[i]):
            if lines[i].rstrip() != new_line.rstrip():
                lines[i] = new_line
            return i
    return -1


def fix_slot(slot, data, by_hr):
    vclusters = data["vclusters"]
    vc = slot.get("vcluster", "host")
    fpath = os.path.join(KIT, slot["file"])
    with open(fpath) as f:
        lines = f.read().splitlines()

    s, e = hr_doc_range(lines, slot["hr"])
    if s is None:
        print(f"  ✗ {slot['file']}: HelmRelease {slot['hr']} doc not found", file=sys.stderr)
        return False

    want_ns = hr_namespace_for(slot, vclusters)

    # 1. metadata.namespace — first `  namespace:` line in the doc
    #    (metadata block precedes spec).
    for i in range(s, e):
        if re.match(r"^  namespace: ", lines[i]):
            lines[i] = f"  namespace: {want_ns}  {GENERATED_MARK}" if lines[i].split(":", 1)[1].split("#")[0].strip() != want_ns else lines[i]
            break

    if vc != "host":
        vci = vclusters[vc]
        # 2. vcluster label
        in_labels = False
        has_label = False
        labels_idx = -1
        for i in range(s, e):
            if re.match(r"^  labels:", lines[i]):
                in_labels = True
                labels_idx = i
                continue
            if in_labels:
                if not lines[i].startswith("    "):
                    break
                if "catalyst.openova.io/vcluster:" in lines[i]:
                    has_label = True
                    lines[i] = f"    catalyst.openova.io/vcluster: {vc}"
        if labels_idx >= 0 and not has_label:
            lines.insert(labels_idx + 1, f"    catalyst.openova.io/vcluster: {vc}")
            e += 1

        # 3. targetNamespace + storageNamespace + kubeConfig
        tn_idx = set_line_value(lines, s, e, r"^  targetNamespace: ",
                                f"  targetNamespace: {slot['namespace']}")
        if tn_idx < 0:
            print(f"  ✗ {slot['file']}: no targetNamespace line to anchor on", file=sys.stderr)
            return False
        text = "\n".join(lines[s:e])
        if not re.search(r"^  storageNamespace: ", text, re.M):
            lines.insert(tn_idx + 1, f"  storageNamespace: {slot['namespace']}  {GENERATED_MARK}")
            e += 1
        else:
            set_line_value(lines, s, e, r"^  storageNamespace: ",
                           f"  storageNamespace: {slot['namespace']}")
        text = "\n".join(lines[s:e])
        if "kubeConfig:" not in text:
            anchor = set_line_value(lines, s, e, r"^  storageNamespace: ",
                                    f"  storageNamespace: {slot['namespace']}")
            block = [
                f"  # vCluster pivot — the ONE generic render mechanism ({GENERATED_MARK.lstrip('# ')})",
                "  kubeConfig:",
                "    secretRef:",
                f"      name: {vci['kubeconfigSecret']}",
                "      key: config",
            ]
            for j, bl in enumerate(block):
                lines.insert(anchor + 1 + j, bl)
            e += len(block)

        # 4. dependsOn — explicit namespaces + the runtime edge.
        dep_idx = -1
        for i in range(s, e):
            if re.match(r"^  dependsOn:", lines[i]):
                dep_idx = i
                break
        if dep_idx < 0:
            # insert a dependsOn block right after kubeConfig key line
            for i in range(s, e):
                if re.match(r"^      key: config", lines[i]):
                    lines[i
                          + 1:i + 1] = ["  dependsOn:"]
                    dep_idx = i + 1
                    e += 1
                    break
        # walk entries
        i = dep_idx + 1
        entries = []  # (name_line_idx, name, ns_line_idx or -1)
        while i < e:
            m = re.match(r"^    - name: (\S+)", lines[i])
            if m:
                name = m.group(1).strip('"')
                ns_idx = -1
                j = i + 1
                while j < e and (lines[j].startswith("      ") or lines[j].strip().startswith("#")):
                    if re.match(r"^      namespace: ", lines[j]):
                        ns_idx = j
                    j += 1
                entries.append((i, name, ns_idx))
                i = j
                continue
            if lines[i].strip() and not lines[i].startswith("    "):
                break
            i += 1
        offset = 0
        for name_idx, name, ns_idx in entries:
            tgt = by_hr.get(name)
            tgt_ns = hr_namespace_for(tgt, vclusters) if tgt else "flux-system"
            if ns_idx >= 0:
                lines[ns_idx + offset] = f"      namespace: {tgt_ns}"
            else:
                lines.insert(name_idx + offset + 1, f"      namespace: {tgt_ns}  {GENERATED_MARK}")
                offset += 1
                e += 1
        if vci["runtimeHR"] not in [n for _, n, _ in entries]:
            ins = [
                f"    # {vci['runtimeHR']} MUST be Ready first — the {vci['kubeconfigSecret']}",
                f"    # Secret doesn't exist until then. {GENERATED_MARK.lstrip('# ')}",
                f"    - name: {vci['runtimeHR']}",
                "      namespace: flux-system",
            ]
            lines[dep_idx + 1:dep_idx + 1] = ins
            e += len(ins)

        # 5. install.createNamespace
        text = "\n".join(lines[s:e])
        if not re.search(r"^    createNamespace: true", text, re.M):
            for i in range(s, e):
                if re.match(r"^  install:", lines[i]):
                    lines.insert(i + 1, f"    createNamespace: true  {GENERATED_MARK}")
                    e += 1
                    break

        # 6. declared placement values (simple dotted scalar stamping)
        for dotted, want in (slot.get("values") or {}).items():
            segs = dotted.split(".")
            # locate `  values:` inside the HR doc
            vidx = -1
            for i in range(s, e):
                if re.match(r"^  values:", lines[i]):
                    vidx = i
                    break
            if vidx < 0:
                lines.insert(e, "  values:")
                vidx = e
                e += 1
            cur = vidx
            indent = 4
            for k, seg in enumerate(segs):
                last = k == len(segs) - 1
                pat = re.compile(rf"^{' ' * indent}{re.escape(seg)}:")
                found = -1
                j = cur + 1
                while j < e and (lines[j].startswith(" " * indent) or not lines[j].strip() or lines[j].strip().startswith("#")):
                    if pat.match(lines[j]):
                        found = j
                        break
                    j += 1
                if last:
                    new = f"{' ' * indent}{seg}: {want}  {GENERATED_MARK}"
                    if found >= 0:
                        lines[found] = new
                    else:
                        lines.insert(cur + 1, new)
                        e += 1
                else:
                    if found >= 0:
                        cur = found
                    else:
                        lines.insert(cur + 1, f"{' ' * indent}{seg}:")
                        cur = cur + 1
                        e += 1
                    indent += 2

    else:
        # host placement: drop kubeConfig block + storageNamespace if a
        # previous vcluster placement left them behind.
        i = s
        while i < e:
            if re.match(r"^  kubeConfig:", lines[i]):
                j = i + 1
                while j < e and (lines[j].startswith("    ") or lines[j].strip().startswith("#")):
                    j += 1
                del lines[i:j]
                e -= j - i
                continue
            if re.match(r"^  storageNamespace: ", lines[i]):
                del lines[i]
                e -= 1
                continue
            i += 1
        set_line_value(lines, s, e, r"^  targetNamespace: ",
                       f"  targetNamespace: {slot['namespace']}")

    with open(fpath, "w") as f:
        f.write("\n".join(lines) + "\n")
    return True


def fix_dependents(data, by_hr):
    """Stamp dependents' cross-namespace edges for re-homed HRs."""
    vclusters = data["vclusters"]
    for slot in data["slots"]:
        fpath = os.path.join(KIT, slot["file"])
        with open(fpath) as f:
            lines = f.read().splitlines()
        s, e = hr_doc_range(lines, slot["hr"])
        if s is None:
            continue
        changed = False
        i = s
        while i < e:
            m = re.match(r"^    - name: (\S+)", lines[i])
            if m:
                name = m.group(1).strip('"')
                tgt = by_hr.get(name)
                if tgt is not None:
                    tgt_ns = hr_namespace_for(tgt, vclusters)
                    ns_idx = -1
                    j = i + 1
                    while j < e and (lines[j].startswith("      ") or lines[j].strip().startswith("#")):
                        if re.match(r"^      namespace: ", lines[j]):
                            ns_idx = j
                        j += 1
                    if ns_idx >= 0:
                        want = f"      namespace: {tgt_ns}"
                        if lines[ns_idx].split("#")[0].rstrip() != want:
                            lines[ns_idx] = f"{want}  {GENERATED_MARK}"
                            changed = True
                    elif tgt_ns != "flux-system" or hr_namespace_for(slot, vclusters) != "flux-system":
                        lines.insert(i + 1, f"      namespace: {tgt_ns}  {GENERATED_MARK}")
                        e += 1
                        changed = True
            i += 1
        if changed:
            with open(fpath, "w") as f:
                f.write("\n".join(lines) + "\n")


def main():
    if len(sys.argv) != 2 or sys.argv[1] not in ("check", "fix"):
        print(__doc__)
        sys.exit(2)
    data, by_hr = load_placement()
    if sys.argv[1] == "check":
        sys.exit(0 if check(data, by_hr) else 1)
    # fix
    ok = True
    for slot in data["slots"]:
        if not fix_slot(slot, data, by_hr):
            ok = False
    fix_dependents(data, by_hr)
    if not ok:
        sys.exit(1)
    if not check(data, by_hr, verbose=False):
        print("fix applied but check still fails — inspect the drift above", file=sys.stderr)
        sys.exit(1)
    print("placement fix applied + check PASSED")


if __name__ == "__main__":
    main()
