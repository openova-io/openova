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
  * the host-bridge block (#3642) — when a vCluster slot declares
    `hostBridge:`, the host-side HTTPRoute / ExternalSecret / CNPG
    `Cluster` docs are transcribed VERBATIM into the slot between the
    HOST-BRIDGE markers, and `hostBridge.suppress` values are stamped
    into the workload HR (turning the chart's OWN in-vCluster
    route/secret/CNPG off — the host-side copies are authoritative).

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

# bootstrap-kit-crs (#3804 / Refs #3642) — the SEPARATE Flux Kustomization
# tier that carries the host-bridge raw CRD-dependent CRs (HTTPRoute,
# ExternalSecret, CNPG Cluster). These CANNOT live in the bootstrap-kit
# kustomize apply set: bootstrap-kit reconciles all ~56 slots atomically and
# its server-side validation pass dry-runs the WHOLE set BEFORE the first
# apply — but the gateway.networking.k8s.io / external-secrets.io /
# postgresql.cnpg.io CRDs are installed by bp-gateway-api / bp-external-
# secrets / bp-cnpg HelmReleases that have NOT reconciled yet on a fresh
# prov's first pass, so the raw CRs fail with `no matches for kind` → the
# entire bootstrap-kit reconcile aborts → 0 HRs ever apply → the operators
# never install → the CRDs never appear → permanent deadlock (diagnosed live
# on hw161; #3642 introduced these raw CRs — pre-#3642 there were none).
# Raw kustomize YAML cannot carry the Helm `.Capabilities.APIVersions.Has`
# gate that the in-chart templates used, so the fix is the platform's
# established CRD-ordering idiom (slots 02a/14/15a/16b/27a + the sovereign-
# tls Kustomization): a SEPARATE Flux Kustomization that `dependsOn:
# [bootstrap-kit]` so it validates+applies these CRs only AFTER bootstrap-kit
# is Ready (operators installed + their CRDs registered). The generator
# renders the host-bridge docs HERE, one file per source slot, instead of
# appending them into the slot file.
CRS_DIR = os.path.join(REPO, "clusters", "_template", "bootstrap-kit-crs")

GENERATED_MARK = "# placement-generated (#3373) — source: placement.yaml; edit THAT file + run scripts/render-slot-placement.py fix"
CRS_GENERATED_HEADER = (
    "# placement-generated (#3804 / Refs #3642) — host-bridge CRD-dependent CRs\n"
    "# for slot %(slot)s. SOURCE OF TRUTH: clusters/_template/bootstrap-kit/\n"
    "# placement.yaml (the matching slot's hostBridge.hostDocuments). Edit THERE\n"
    "# + run scripts/render-slot-placement.py fix — never hand-edit this file.\n"
    "#\n"
    "# Reconciled by the `bootstrap-kit-crs` Flux Kustomization (defined in\n"
    "# infra/providers/_shared/cloudinit-control-plane.tftpl), which\n"
    "# `dependsOn: [bootstrap-kit]` so these gateway.networking.k8s.io /\n"
    "# external-secrets.io / postgresql.cnpg.io CRs are validated + applied\n"
    "# only AFTER their operator CRDs exist — never in the bootstrap-kit atomic\n"
    "# dry-run (the fresh-prov `no matches for kind` deadlock, hw161)."
)

# ── host-bridge markers (#3642) ─────────────────────────────────────
# An app re-homed INTO a vCluster ships its public HTTPRoute, its
# ExternalSecret, and (where applicable) its CNPG `Cluster` CR from
# inside its own chart — which, post-pivot, renders those CRs inside
# the vCluster apiserver where the HOST Cilium Gateway / external-
# secrets-operator / cnpg-operator (all host-minimum substrate) can
# never see them. The OSS vCluster syncer's `sync.toHost.customResources`
# mirror that would carry them host-ward is Pro/Platform-gated and INERT
# on the pure-OSS build the Sovereign runs (placement.yaml §4 exception
# block) — so the route would resolve nowhere and the secret would never
# materialise.
#
# The host-bridge (#3642, plan path (B)) keeps those host-reaching CRs
# HOST-rendered: the slot declares them as data under `hostBridge:` and
# this generator transcribes them VERBATIM into the slot file between the
# markers below, in the app's HOST namespace, with every `backendRef`
# pre-aliased to the syncer-mangled Service `<svc>-x-<innerNs>-x-<sfx>`
# (the OSS `sync.toHost.services` mirror, proven by loki/nats). The
# workload HR (with spec.kubeConfig.secretRef → vc-<tier>) re-homes only
# the Deployment/StatefulSet; `hostBridge.suppress` stamps the chart
# values that turn the chart's OWN in-vCluster route/secret/CNPG off so
# the host-side copies are authoritative (no double-render).
#
# Placement stays DATA: the route/secret/CNPG content lives in
# placement.yaml; the slot block is a pure transcription of it, regen-ed
# on every `fix` and drift-gated on every `check`.
HOSTBRIDGE_BEGIN = "# ── HOST-BRIDGE BEGIN (#3642) — placement-generated host-side route/secret/CNPG; edit placement.yaml hostBridge: + run scripts/render-slot-placement.py fix ──"
HOSTBRIDGE_END = "# ── HOST-BRIDGE END (#3642) ──"


def yaml_scalar(v):
    """Render a placement value as a canonical YAML scalar — lowercase
    booleans (`false`, not Python's `False`), so the stamped slot is
    valid + idiomatic YAML (#3642)."""
    if isinstance(v, bool):
        return "true" if v else "false"
    return str(v)


def effective_values(slot):
    """The placement-declared HR values the generator stamps — the slot's
    own `values:` block PLUS the host-bridge `suppress:` overrides that
    turn the chart's in-vCluster route/secret/CNPG off (#3642). Suppress
    wins on key collision (it is the placement decision)."""
    vals = dict(slot.get("values") or {})
    hb = slot.get("hostBridge") or {}
    vals.update(hb.get("suppress") or {})
    return vals


def crs_file_for(slot):
    """The bootstrap-kit-crs/ file path that holds this slot's host-bridge
    CRs (#3804). One file per source slot, same basename so the provenance
    is obvious (e.g. bootstrap-kit/10-gitea.yaml -> bootstrap-kit-crs/
    10-gitea.yaml)."""
    return os.path.join(CRS_DIR, slot["file"])


def _crs_body(text):
    """Return the YAML body of a generated bootstrap-kit-crs file — i.e.
    everything from the first `---` document separator onward, dropping the
    leading generated-header comment block (#3804). Robust to header text
    changes: the body always starts at the first `---` line."""
    lines = text.splitlines()
    for i, l in enumerate(lines):
        if l.rstrip() == "---":
            return "\n".join(lines[i:])
    return ""


def crs_files_expected(data):
    """The set of bootstrap-kit-crs/<basename> files the placement implies —
    every vCluster slot that declares hostBridge.hostDocuments (#3804)."""
    out = []
    for slot in data["slots"]:
        if slot.get("vcluster", "host") != "host" and hostbridge_docs_yaml(slot) is not None:
            out.append(slot["file"])
    return out


def hostbridge_docs_yaml(slot):
    """The rendered YAML body (a string) of this slot's host-bridge CRs —
    each `hostBridge.hostDocuments` entry as its own `---`-separated doc —
    or None when the slot declares no host-bridge. Verbatim transcription
    of the placement data: placement is data, the file is its rendering
    (#3804 / Refs #3642)."""
    hb = slot.get("hostBridge") or {}
    docs = hb.get("hostDocuments") or []
    if not docs:
        return None
    out = []
    for doc in docs:
        out.append("---")
        # block-style, 2-space indent, keys in declared order (yaml lib
        # preserves dict insertion order on py3.7+).
        rendered = yaml.safe_dump(doc, default_flow_style=False, sort_keys=False).rstrip("\n")
        out.extend(rendered.splitlines())
    return "\n".join(out)


def hostbridge_block(slot):
    """DEPRECATED in-slot rendering (#3804). Pre-#3804 the host-bridge CRs
    were transcribed INTO the slot file between these markers; they now live
    in their own bootstrap-kit-crs/<slot> file (see hostbridge_docs_yaml +
    fix_hostbridge). Retained only so strip_hostbridge / extract_hostbridge
    can detect + remove any stale in-slot block on a slot authored before
    the split. Returns None always — no slot should carry an in-slot block
    anymore."""
    return None


def extract_hostbridge(lines):
    """Return the host-bridge marker block currently in a slot file (the
    list of lines from BEGIN..END inclusive), or None if absent (#3642)."""
    begin = end = None
    for i, l in enumerate(lines):
        if l.rstrip() == HOSTBRIDGE_BEGIN:
            begin = i
        elif l.rstrip() == HOSTBRIDGE_END:
            end = i
            break
    if begin is None or end is None or end < begin:
        return None
    return [l.rstrip("\n") for l in lines[begin:end + 1]]


def strip_hostbridge(lines):
    """Remove an existing host-bridge block (markers inclusive) plus the
    blank line that precedes it, returning the cleaned list (#3642)."""
    block = extract_hostbridge(lines)
    if block is None:
        return lines
    begin = next(i for i, l in enumerate(lines) if l.rstrip() == HOSTBRIDGE_BEGIN)
    end = next(i for i, l in enumerate(lines) if l.rstrip() == HOSTBRIDGE_END)
    lo = begin
    while lo > 0 and lines[lo - 1].strip() == "":
        lo -= 1
    hi = end + 1
    return lines[:lo] + lines[hi:]


def load_placement():
    with open(PLACEMENT) as f:
        data = yaml.safe_load(f)
    # Typed-document form (apiVersion placement.catalyst.openova.io,
    # kind PlacementTable): the table lives under spec. The bare legacy
    # form (top-level vclusters/slots) stays accepted.
    if data and "spec" in data and "slots" not in data:
        data = data["spec"]
    if not data or "slots" not in data or "vclusters" not in data:
        sys.exit("placement.yaml malformed: needs vclusters: + slots: (optionally under spec:)")
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

        # declared placement values stamped? (#3642: the slot's own
        # values PLUS the host-bridge suppress overrides)
        for dotted, want in effective_values(slot).items():
            got = get_dotted(spec.get("values") or {}, dotted)
            if got != want:
                errors.append(f"{slot['file']}: values.{dotted} = {got!r}, placement declares {want!r}")

        # host-bridge CRs (#3804 / Refs #3642): the host-side route/secret/
        # CNPG docs the placement declares are NO LONGER transcribed into the
        # slot file — they live in their own bootstrap-kit-crs/<slot> file
        # reconciled by the bootstrap-kit-crs Kustomization (dependsOn
        # bootstrap-kit) so they validate only AFTER their operator CRDs
        # exist. Two drift gates:
        #   (1) NO slot file may carry a (now-stale) in-slot host-bridge block.
        #   (2) the bootstrap-kit-crs/<slot> file must equal EXACTLY the docs
        #       the placement declares (verbatim transcription); a host
        #       placement (or a vcluster slot with no hostDocuments) must NOT
        #       have a crs file.
        with open(fpath) as f:
            raw_lines = f.read().splitlines()
        got_hb = extract_hostbridge(raw_lines)
        if got_hb is not None:
            errors.append(f"{slot['file']}: carries a stale in-slot host-bridge block — the "
                          f"host-bridge CRs moved to bootstrap-kit-crs/{slot['file']} (#3804); "
                          f"run scripts/render-slot-placement.py fix to strip it")
        want_docs = hostbridge_docs_yaml(slot) if vc != "host" else None
        crs_path = crs_file_for(slot)
        if vc == "host" and (slot.get("hostBridge") or {}).get("hostDocuments"):
            errors.append(f"placement.yaml: {slot['hr']} is a host placement but declares "
                          f"hostBridge.hostDocuments — only vCluster placements need the "
                          f"host-bridge (host placements render their CRs natively)")
        if want_docs is None:
            if os.path.exists(crs_path):
                errors.append(f"bootstrap-kit-crs/{slot['file']}: exists but placement.yaml "
                              f"declares no hostBridge.hostDocuments for {slot['hr']} "
                              f"(run scripts/render-slot-placement.py fix to remove it)")
        else:
            if not os.path.exists(crs_path):
                errors.append(f"bootstrap-kit-crs/{slot['file']}: MISSING — placement.yaml "
                              f"declares hostBridge.hostDocuments for {slot['hr']} but the "
                              f"host-bridge CRs file is absent (run "
                              f"scripts/render-slot-placement.py fix)")
            else:
                with open(crs_path) as f:
                    got_body = f.read()
                # strip the generated header (everything up to + including the
                # first blank line after the leading comment block) for the
                # body comparison.
                got_docs = _crs_body(got_body)
                if got_docs.rstrip("\n") != want_docs.rstrip("\n"):
                    errors.append(f"bootstrap-kit-crs/{slot['file']}: host-bridge CRs drift — "
                                  f"does not match placement.yaml hostBridge.hostDocuments "
                                  f"(run scripts/render-slot-placement.py fix)")

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

    # bootstrap-kit-crs/kustomization.yaml completeness (#3804): the crs-tier
    # Kustomization must list EXACTLY the per-slot CR files the placement
    # implies — an unlisted file is INERT (Flux skips it → the host-bridge
    # route/secret/CNPG never apply → the re-homed app has no host front
    # door), and a listed-but-absent file fails the kustomize build. Mirrors
    # the Phase-2.5 slot-registration guard in check-bootstrap-deps.sh.
    expected_crs = set(crs_files_expected(data))
    crs_kustomization = os.path.join(CRS_DIR, "kustomization.yaml")
    if expected_crs and not os.path.exists(crs_kustomization):
        errors.append("bootstrap-kit-crs/kustomization.yaml: MISSING but placement declares "
                      "host-bridge CRs (run scripts/render-slot-placement.py fix)")
    elif os.path.exists(crs_kustomization):
        with open(crs_kustomization) as f:
            kdata = yaml.safe_load(f) or {}
        listed = set(kdata.get("resources") or [])
        for missing in sorted(expected_crs - listed):
            errors.append(f"bootstrap-kit-crs/kustomization.yaml: resources[] is missing "
                          f"'{missing}' — the host-bridge CRs file is INERT until listed "
                          f"(run scripts/render-slot-placement.py fix)")
        for extra in sorted(listed - expected_crs):
            errors.append(f"bootstrap-kit-crs/kustomization.yaml: resources[] lists '{extra}' "
                          f"but placement declares no host-bridge CRs for it "
                          f"(run scripts/render-slot-placement.py fix)")
    # stray crs files not implied by placement
    if os.path.isdir(CRS_DIR):
        for fn in sorted(os.listdir(CRS_DIR)):
            if not re.match(r"^[0-9].*\.ya?ml$", fn):
                continue
            if fn not in expected_crs:
                errors.append(f"bootstrap-kit-crs/{fn}: present but placement.yaml declares no "
                              f"host-bridge CRs for it (run scripts/render-slot-placement.py fix)")

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
                # #3642 idempotency: only rewrite when the value actually
                # changes — an unconditional rewrite strips a correct line's
                # trailing comment and churns the slot on every `fix`.
                if lines[ns_idx + offset].split("#")[0].rstrip() != f"      namespace: {tgt_ns}":
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

        # 6. declared placement values (simple dotted scalar stamping) —
        #    the slot's own values + host-bridge suppress overrides (#3642)
        for dotted, want in effective_values(slot).items():
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
                    new = f"{' ' * indent}{seg}: {yaml_scalar(want)}  {GENERATED_MARK}"
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


def fix_hostbridge(data):
    """Render each vCluster slot's host-bridge route/secret/CNPG docs into
    its OWN bootstrap-kit-crs/<slot> file (#3804 / Refs #3642) — NOT into the
    slot file (raw CRD-dependent CRs in the bootstrap-kit atomic apply set
    deadlock a fresh prov; the crs tier is a separate Flux Kustomization that
    dependsOn bootstrap-kit). Also:
      * strips any stale in-slot host-bridge block left by the pre-#3804
        in-slot rendering;
      * removes a crs file for a slot that no longer declares hostDocuments
        (or reverted to host placement);
      * regenerates bootstrap-kit-crs/kustomization.yaml to list exactly the
        per-slot CR files.
    Idempotent."""
    # 1. strip any stale in-slot block from EVERY slot file (back-compat with
    #    the pre-#3804 in-slot rendering).
    for slot in data["slots"]:
        fpath = os.path.join(KIT, slot["file"])
        with open(fpath) as f:
            lines = f.read().splitlines()
        stripped = strip_hostbridge(lines)
        if stripped != lines:
            with open(fpath, "w") as f:
                f.write("\n".join(stripped) + "\n")

    # 2. (re)write the crs files; track which slots have one.
    os.makedirs(CRS_DIR, exist_ok=True)
    expected = crs_files_expected(data)
    expected_set = set(expected)
    for slot in data["slots"]:
        crs_path = crs_file_for(slot)
        if slot["file"] in expected_set:
            body = hostbridge_docs_yaml(slot)
            header = CRS_GENERATED_HEADER % {"slot": slot["file"]}
            with open(crs_path, "w") as f:
                f.write(header + "\n" + body + "\n")
        elif os.path.exists(crs_path):
            os.remove(crs_path)

    # 3. drop any stray generated crs file no longer implied by placement.
    for fn in sorted(os.listdir(CRS_DIR)):
        if re.match(r"^[0-9].*\.ya?ml$", fn) and fn not in expected_set:
            os.remove(os.path.join(CRS_DIR, fn))

    # 4. regenerate the crs kustomization.yaml (deterministic slot order).
    kustomization = (
        "# placement-generated (#3804 / Refs #3642) — DO NOT hand-edit.\n"
        "# The bootstrap-kit-crs Flux Kustomization tier: host-bridge\n"
        "# CRD-dependent CRs (HTTPRoute / ExternalSecret / CNPG Cluster) for\n"
        "# vCluster-re-homed apps. Reconciled by the `bootstrap-kit-crs`\n"
        "# Kustomization (cloud-init flux-bootstrap), which dependsOn\n"
        "# bootstrap-kit so these CRs validate + apply only AFTER their\n"
        "# operator CRDs exist — keeping them OUT of the bootstrap-kit atomic\n"
        "# dry-run that would otherwise fail `no matches for kind` on a fresh\n"
        "# prov (hw161). Source of truth: bootstrap-kit/placement.yaml\n"
        "# hostBridge.hostDocuments + scripts/render-slot-placement.py fix.\n"
        "apiVersion: kustomize.config.k8s.io/v1beta1\n"
        "kind: Kustomization\n"
        "resources:\n"
    )
    kustomization += "".join(f"  - {fn}\n" for fn in expected)
    with open(os.path.join(CRS_DIR, "kustomization.yaml"), "w") as f:
        f.write(kustomization)


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
    fix_hostbridge(data)  # #3642 host-bridge route/secret/CNPG transcription
    if not ok:
        sys.exit(1)
    if not check(data, by_hr, verbose=False):
        print("fix applied but check still fails — inspect the drift above", file=sys.stderr)
        sys.exit(1)
    print("placement fix applied + check PASSED")


if __name__ == "__main__":
    main()
