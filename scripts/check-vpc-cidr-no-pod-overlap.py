#!/usr/bin/env python3
"""check-vpc-cidr-no-pod-overlap.py — the hashed Huawei node-VPC CIDR must never
land on the k3s pod / service overlay, for EVERY hash value and region count.

Refs #6786 / #6778 (hw306, deployment e68e79721ecbde62, 2026-09-02):
infra/providers/huawei/main.tf derives each region's node-VPC second octet
from sha256(deployment_id) — `base = 32 + (byte % (216 - (N-1)*10))`, region
idx at `base + idx*10` — while the k3s pod CIDRs are FIXED at 10.(42+idx)/16
and the service CIDRs at 10.(96+idx)/16. e68e79721ecbde62 hashed to base 42,
so region-a's node VPC was 10.42.0.0/16 == region-a's pod CIDR; the peering
route for the VPC CIDR then duplicated the pod-CIDR route on the same route
table and `tofu apply` died with VPC.2812. 8 of 206 two-region buckets
(3.9 %) had this shape and nothing measured it — `tofu validate`, `tofu test`
and the render harness all passed, because the defect only exists for
specific hash values.

The fix keeps the raw bucket and shifts the base UP by N when any region
octet lands in a band (proof in the main.tf comment). This guard is the
enumeration behind that proof:

  (a) it PARSES the literals (32, 216, 10, 42, 96, the shift) out of main.tf
      instead of hard-coding them, so a change to either formula that is
      not mirrored here fails as PARSE DRIFT (exit 2) rather than passing on
      a stale model;
  (b) it evaluates the corrected logic for all 256 first-byte values and
      N in 1..5 (the proof bound; the fleet runs N in {1,2,3});
  (c) it asserts zero band collisions, no overflow past the OVERFLOW-FIX
      ceiling (last region <= 247), no underflow below 32, and that a
      non-colliding hash is left EXACTLY where it was (existing Sovereigns
      keep their CIDRs);
  (d) it pins e68e79721ecbde62 as a named regression (raw 42 -> 44/54) plus
      hw305 b2b00ce4c833badf (66, untouched) and the tftest id
      deadbeefcafef00d (127, untouched) as controls.

--self-test proves the detector is not vacuous: the PRE-fix formula (shift
disabled) must report the known colliding set, and a mutated main.tf whose
shift was removed or whose band literal drifted must be REJECTED.

Run:  scripts/check-vpc-cidr-no-pod-overlap.py [--self-test]
Exit: 0 clean · 1 collision / overflow / regression · 2 parse drift or setup
"""

import hashlib
import os
import pathlib
import re
import sys
from dataclasses import dataclass

ROOT = pathlib.Path(os.environ.get("ROOT", "."))
MAIN_TF = ROOT / "infra" / "providers" / "huawei" / "main.tf"

# Proof bound from the main.tf comment: the +N shift is collision-free for
# 1 <= N <= 5 (the k < 0 case needs 2N-1-10 < 0). The fleet runs 1..3.
N_MAX = 5

# Named regression + controls: (deployment_id, N, expected raw, expected
# shifted octets). Values come from the incident, not from this model.
NAMED_CASES = [
    ("e68e79721ecbde62", 2, 42, [44, 54]),  # hw306 — the VPC.2812 failure
    ("b2b00ce4c833badf", 2, 66, [66, 76]),  # hw305 — worked, must not move
    ("deadbeefcafef00d", 2, 127, [127, 137]),  # tests/multi_region.tftest.hcl
]
# The colliding raw bases for N=2 as stated in the incident (#6786).
EXPECTED_COLLIDING_N2 = [32, 33, 42, 43, 86, 87, 96, 97]

# ── (a) parse the formula out of main.tf ────────────────────────────────
RAW_RE = re.compile(
    r"^\s*cidr_base_raw\s*=\s*(?P<lo>\d+)\s*\+\s*\(parseint\(substr\(sha256\(var\.deployment_id\),"
    r"\s*(?P<sub_off>\d+),\s*(?P<sub_len>\d+)\),\s*16\)\s*%\s*\((?P<span>\d+)\s*-\s*"
    r"\(length\(var\.regions\)\s*-\s*1\)\s*\*\s*(?P<step>\d+)\)\)\s*$",
    re.M,
)
VPC_RE = re.compile(r'format\("10\.%d\.0\.0/16",\s*local\.cidr_base\s*\+\s*idx\s*\*\s*(\d+)\)')
SUBNET_RE = re.compile(r'format\("10\.%d\.1\.0/24",\s*local\.cidr_base\s*\+\s*idx\s*\*\s*(\d+)\)')
POD_RE = re.compile(r'region_cluster_cidr\s*=\s*\{[^}]*?format\("10\.%d\.0\.0/16",\s*(\d+)\s*\+\s*idx\)', re.S)
SVC_RE = re.compile(r'region_service_cidr\s*=\s*\{[^}]*?format\("10\.%d\.0\.0/16",\s*(\d+)\s*\+\s*idx\)', re.S)
HIT_RE = re.compile(r"cidr_base_hits_overlay\s*=\s*anytrue\(\[(?P<body>.*?)\]\)", re.S)
BAND_RE = re.compile(
    r"local\.cidr_base_raw\s*\+\s*idx\s*\*\s*(?P<step1>\d+)\s*>=\s*(?P<lo>\d+)\s*&&\s*"
    r"local\.cidr_base_raw\s*\+\s*idx\s*\*\s*(?P<step2>\d+)\s*<=\s*(?P<lo2>\d+)\s*\+\s*length\(var\.regions\)\s*-\s*1"
)
SHIFT_RE = re.compile(
    r"^\s*cidr_base\s*=\s*local\.cidr_base_raw\s*\+\s*\(local\.cidr_base_hits_overlay\s*\?\s*"
    r"length\(var\.regions\)\s*:\s*0\)\s*$",
    re.M,
)


@dataclass(frozen=True)
class Formula:
    lo: int  # 32
    span: int  # 216
    step: int  # 10 (per-region offset)
    pod: int  # 42
    svc: int  # 96
    ceiling: int  # lo + span - 1 == 247


class ParseDrift(Exception):
    pass


def parse_formula(text: str) -> Formula:
    m = RAW_RE.search(text)
    if not m:
        raise ParseDrift("cidr_base_raw line not found or its shape changed — update the model in this guard")
    if (int(m["sub_off"]), int(m["sub_len"])) != (0, 2):
        raise ParseDrift("cidr_base_raw no longer hashes exactly the first byte (substr 0,2)")
    lo, span, step = int(m["lo"]), int(m["span"]), int(m["step"])

    if not VPC_RE.search(text) or not SUBNET_RE.search(text):
        raise ParseDrift("region_vpc_cidr / region_subnet_cidr no longer use local.cidr_base + idx * <step>")
    steps = {int(x) for x in VPC_RE.findall(text)} | {int(x) for x in SUBNET_RE.findall(text)}
    if steps != {step}:
        raise ParseDrift(f"per-region step differs between cidr_base_raw ({step}) and region_vpc/subnet ({sorted(steps)})")

    pod_m, svc_m = POD_RE.search(text), SVC_RE.search(text)
    if not pod_m or not svc_m:
        raise ParseDrift('region_cluster_cidr / region_service_cidr no longer read as format("10.%d.0.0/16", <base> + idx)')
    pod, svc = int(pod_m.group(1)), int(svc_m.group(1))

    hit_m = HIT_RE.search(text)
    if not hit_m:
        raise ParseDrift("cidr_base_hits_overlay anytrue([...]) block not found")
    bands = []
    for b in BAND_RE.finditer(hit_m["body"]):
        if b["lo"] != b["lo2"] or b["step1"] != b["step2"] or int(b["step1"]) != step:
            raise ParseDrift(f"band clause is not [<lo>, <lo>+N-1] with step {step}: {b.group(0)!r}")
        bands.append(int(b["lo"]))
    if sorted(bands) != sorted([pod, svc]):
        raise ParseDrift(
            f"band literals in cidr_base_hits_overlay {sorted(bands)} != pod/service bases "
            f"{[pod, svc]} in region_cluster_cidr/region_service_cidr — the two sites drifted"
        )
    if not SHIFT_RE.search(text):
        raise ParseDrift(
            "cidr_base is no longer `cidr_base_raw + (hits_overlay ? length(var.regions) : 0)` — update the model AND the proof"
        )
    return Formula(lo=lo, span=span, step=step, pod=pod, svc=svc, ceiling=lo + span - 1)


# ── (b) the model — one function, mirrors the HCL line for line ──────────
def evaluate(byte: int, n: int, f: Formula, shift: bool = True):
    """Return (raw, hit, octets) for one hash byte and region count."""
    raw = f.lo + (byte % (f.span - (n - 1) * f.step))
    hit = any(band <= raw + idx * f.step <= band + n - 1 for band in (f.pod, f.svc) for idx in range(n))
    base = raw + (n if (shift and hit) else 0)
    return raw, hit, [base + idx * f.step for idx in range(n)]


def in_band(octet: int, n: int, f: Formula) -> bool:
    return f.pod <= octet <= f.pod + n - 1 or f.svc <= octet <= f.svc + n - 1


def first_byte(deployment_id: str) -> int:
    return int(hashlib.sha256(deployment_id.encode()).hexdigest()[:2], 16)


# ── (c)+(d) the assertions ───────────────────────────────────────────────
def run(f: Formula) -> int:
    fails = 0

    def fail(msg):
        nonlocal fails
        fails += 1
        print(f"FAIL: {msg}", file=sys.stderr)

    print(f"parsed: lo={f.lo} span={f.span} step={f.step} pod={f.pod} svc={f.svc} ceiling={f.ceiling} shift=+N")
    for n in range(1, N_MAX + 1):
        shifted = []
        for byte in range(256):
            raw, hit, octets = evaluate(byte, n, f)
            for idx, o in enumerate(octets):
                if in_band(o, n, f):
                    fail(f"N={n} byte={byte} raw={raw}: region {idx} octet 10.{o}.0.0/16 is inside the pod/service band")
            if max(octets) > f.ceiling:
                fail(f"N={n} byte={byte} raw={raw}: last region octet {max(octets)} > ceiling {f.ceiling}")
            if min(octets) < f.lo:
                fail(f"N={n} byte={byte} raw={raw}: first region octet {min(octets)} < floor {f.lo}")
            if len(set(octets)) != n:
                fail(f"N={n} byte={byte} raw={raw}: region octets not distinct {octets}")
            if not hit and octets[0] != raw:
                fail(f"N={n} byte={byte}: non-colliding raw {raw} was moved to {octets[0]} — existing Sovereigns would re-CIDR")
            if hit:
                shifted.append(raw)
        buckets = f.span - (n - 1) * f.step
        moved = sorted(set(shifted))
        print(f"N={n}: {buckets} raw buckets, {len(moved)} colliding raw bases shifted by +{n}: {moved}")
        if n == 2 and moved != EXPECTED_COLLIDING_N2:
            fail(f"N=2 colliding set {moved} != the incident's {EXPECTED_COLLIDING_N2}")

    for dep, n, want_raw, want_octets in NAMED_CASES:
        raw, hit, octets = evaluate(first_byte(dep), n, f)
        tag = "shifted" if hit else "untouched"
        print(f"{dep} N={n}: byte={first_byte(dep)} raw={raw} -> {octets} ({tag})")
        if raw != want_raw or octets != want_octets:
            fail(f"{dep} N={n}: expected raw {want_raw} -> {want_octets}, got raw {raw} -> {octets}")

    return 1 if fails else 0


# ── --self-test: the detector must SEE the defect, and the parser must
#    REJECT a drifted main.tf. A guard that cannot fail is theater.
def self_test(text: str) -> int:
    f = parse_formula(text)
    bad = 0

    # 1. pre-fix formula (shift disabled) must collide — on the incident id
    #    and on exactly the documented N=2 set.
    raw, hit, octets = evaluate(first_byte("e68e79721ecbde62"), 2, f, shift=False)
    if not (raw == 42 and hit and in_band(octets[0], 2, f)):
        print(f"SELF-TEST FAIL: pre-fix e68e79721ecbde62 did not collide (raw={raw} octets={octets})", file=sys.stderr)
        bad += 1
    pre = sorted({evaluate(b, 2, f, shift=False)[0] for b in range(256) if evaluate(b, 2, f, shift=False)[1]})
    if pre != EXPECTED_COLLIDING_N2:
        print(f"SELF-TEST FAIL: pre-fix N=2 colliding set {pre} != {EXPECTED_COLLIDING_N2}", file=sys.stderr)
        bad += 1

    # 2. a main.tf whose shift was removed must be rejected as drift.
    mutated = re.sub(r"\(local\.cidr_base_hits_overlay \? length\(var\.regions\) : 0\)", "0", text)
    if mutated == text:
        print("SELF-TEST FAIL: could not synthesise the shift-removed mutation", file=sys.stderr)
        bad += 1
    else:
        try:
            parse_formula(mutated)
            print("SELF-TEST FAIL: shift removed from cidr_base but the parser accepted it", file=sys.stderr)
            bad += 1
        except ParseDrift:
            pass

    # 3. a pod base changed in region_cluster_cidr but NOT in the hit clause
    #    must be rejected (the two sites drifted).
    mutated = text.replace('format("10.%d.0.0/16", 42 + idx)', 'format("10.%d.0.0/16", 44 + idx)')
    if mutated == text:
        print("SELF-TEST FAIL: could not synthesise the pod-base drift mutation", file=sys.stderr)
        bad += 1
    else:
        try:
            parse_formula(mutated)
            print("SELF-TEST FAIL: pod base drifted between the two sites but the parser accepted it", file=sys.stderr)
            bad += 1
        except ParseDrift:
            pass

    # 4. the band detector must fire on the pre-fix formula across every N
    #    (in_band() is the assertion the real run relies on).
    saw = 0
    for n in range(1, N_MAX + 1):
        for b in range(256):
            _, _, octets = evaluate(b, n, f, shift=False)
            saw += any(in_band(o, n, f) for o in octets)
    if saw == 0:
        print("SELF-TEST FAIL: in_band() never fires on the pre-fix formula", file=sys.stderr)
        bad += 1

    if bad:
        return 2
    print(
        f"SELF-TEST OK: pre-fix formula collides on {pre} (N=2), drifted main.tf rejected, "
        f"detector live ({saw} pre-fix hits over N=1..{N_MAX})"
    )
    return 0


def main(argv) -> int:
    if not MAIN_TF.is_file():
        print(f"ERROR: {MAIN_TF} not found (run from the repo root or set ROOT)", file=sys.stderr)
        return 2
    text = MAIN_TF.read_text(encoding="utf-8")
    if "--self-test" in argv:
        return self_test(text)
    try:
        f = parse_formula(text)
    except ParseDrift as e:
        print(f"PARSE DRIFT: {e}", file=sys.stderr)
        return 2
    rc = run(f)
    if rc == 0:
        print(f"OK: no node-VPC octet can land on the pod/service overlay for any hash byte, N=1..{N_MAX}")
    else:
        print("FAILED")
    return rc


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
