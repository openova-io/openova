#!/usr/bin/env python3
"""check-network-registry.py - check a Sovereign's network registry (NET-100).

The network registry is the output `network_registry` of every provider module
under infra/providers/: a JSON object with the pod supernet, the MTU, the
region separation, the control-plane access lists, and per region the cluster
name and identifier, the node, pod and service ranges, the mesh endpoint, the
edge address and the egress addresses. Its form is described in
infra/providers/PROVIDER-INTERFACE.md, section 2.

Usage:
  check-network-registry.py <file>     check one document
  check-network-registry.py --self-test
                                       check every document under
                                       scripts/tests/check-network-registry/

<file> holds the registry object, or the output of
`tofu output -json network_registry`, which is the object as a JSON string.

Each violation prints one line: `<rule>: <what is wrong>`. The rules:
  document-shape                        the fields, their types and ranges
  unique-cluster-id                     no two regions share clusterID
  unique-cluster-name                   no two regions share clusterName
  unique-ranges                         no two ranges of the document overlap
                                        (every nodeCIDR, podCIDR, serviceCIDR)
  supernet-covers-pods                  every podCIDR lies inside podSupernet
  supernet-excludes-nodes-and-services  no nodeCIDR or serviceCIDR intersects
                                        podSupernet
  mesh-endpoint-when-multi-region       with more than one region, every
                                        region has meshEndpoint
  no-any-source                         no access list holds a range of
                                        prefix length 0 (0.0.0.0/0, ::/0)
A document that breaks document-shape is not checked by the other rules.

--self-test: every file valid-*.json under scripts/tests/check-network-registry/
must pass, and every file invalid/<rule>.json must fail with a line for
<rule>. Each rule has such a file.

Exit codes: 0 valid (self-test: every case as expected), 1 violations
(self-test: a case not as expected), 2 usage error or unreadable file.
Standard library only.
"""

import ipaddress
import json
import pathlib
import re
import sys

RULES = (
    "document-shape",
    "unique-cluster-id",
    "unique-cluster-name",
    "unique-ranges",
    "supernet-covers-pods",
    "supernet-excludes-nodes-and-services",
    "mesh-endpoint-when-multi-region",
    "no-any-source",
)

TOP_KEYS = {"podSupernet", "mtu", "regionSeparation", "controlPlaneAccess", "regions"}
ACCESS_KEYS = {"adminCIDRs", "provisionerCIDRs"}
REGION_KEYS = {
    "code",
    "cloudRegion",
    "az",
    "clusterName",
    "clusterID",
    "nodeCIDR",
    "podCIDR",
    "serviceCIDR",
    "edgeAddress",
    "egressAddresses",
}
REGION_OPTIONAL = {"meshEndpoint"}
MESH_KEYS = {"ips", "port"}
SEPARATIONS = ("none", "zone", "site")
LABEL = re.compile(r"^[a-z0-9]([-a-z0-9]*[a-z0-9])?$")
RANGE_FIELDS = ("nodeCIDR", "podCIDR", "serviceCIDR")

SELF_TEST_DIR = pathlib.Path(__file__).resolve().parent / "tests" / "check-network-registry"


def is_int(v):
    return isinstance(v, int) and not isinstance(v, bool)


def is_text(v):
    return isinstance(v, str) and v != ""


def ipv4_net(v):
    """An IPv4 network in strict form (no host bits), or None."""
    if not isinstance(v, str):
        return None
    try:
        n = ipaddress.ip_network(v, strict=True)
    except ValueError:
        return None
    return n if n.version == 4 else None


def any_net(v):
    """An IPv4 or IPv6 network in strict form, or None."""
    if not isinstance(v, str):
        return None
    try:
        return ipaddress.ip_network(v, strict=True)
    except ValueError:
        return None


def ipv4_addr(v):
    if not isinstance(v, str):
        return False
    try:
        return ipaddress.ip_address(v).version == 4
    except ValueError:
        return False


def check_keys(where, obj, required, optional, out):
    missing = sorted(required - obj.keys())
    unknown = sorted(obj.keys() - required - optional)
    if missing:
        out.append(f"document-shape: {where} lacks {', '.join(missing)}")
    if unknown:
        out.append(f"document-shape: {where} has unknown field {', '.join(unknown)}")


def check_shape(doc):
    """Return the document-shape violations."""
    out = []
    if not isinstance(doc, dict):
        return ["document-shape: the document is not a JSON object"]
    check_keys("the document", doc, TOP_KEYS, set(), out)

    if "podSupernet" in doc and ipv4_net(doc["podSupernet"]) is None:
        out.append(f"document-shape: podSupernet {doc['podSupernet']!r} is not an IPv4 CIDR")
    if "mtu" in doc and not (is_int(doc["mtu"]) and 1280 <= doc["mtu"] <= 9000):
        out.append(f"document-shape: mtu {doc['mtu']!r} is not an integer from 1280 to 9000")
    if "regionSeparation" in doc and doc["regionSeparation"] not in SEPARATIONS:
        out.append(f"document-shape: regionSeparation {doc['regionSeparation']!r} is not one of none, zone, site")

    access = doc.get("controlPlaneAccess")
    if "controlPlaneAccess" in doc:
        if not isinstance(access, dict):
            out.append("document-shape: controlPlaneAccess is not an object")
        else:
            check_keys("controlPlaneAccess", access, ACCESS_KEYS, set(), out)
            for name in sorted(ACCESS_KEYS & access.keys()):
                lst = access[name]
                if not isinstance(lst, list):
                    out.append(f"document-shape: controlPlaneAccess.{name} is not a list")
                    continue
                for c in lst:
                    if any_net(c) is None:
                        out.append(f"document-shape: controlPlaneAccess.{name} entry {c!r} is not a CIDR")

    regions = doc.get("regions")
    if "regions" in doc:
        if not isinstance(regions, list) or not 1 <= len(regions) <= 6:
            out.append("document-shape: regions is not a list of 1 to 6 entries")
        else:
            for i, r in enumerate(regions):
                out.extend(check_region_shape(i, r))
    return out


def check_region_shape(i, r):
    out = []
    where = f"regions[{i}]"
    if not isinstance(r, dict):
        return [f"document-shape: {where} is not an object"]
    check_keys(where, r, REGION_KEYS, REGION_OPTIONAL, out)
    for name in ("code", "cloudRegion", "az"):
        if name in r and not is_text(r[name]):
            out.append(f"document-shape: {where}.{name} is not a non-empty string")
    if "clusterName" in r:
        v = r["clusterName"]
        if not (isinstance(v, str) and len(v) <= 32 and LABEL.match(v)):
            out.append(f"document-shape: {where}.clusterName {v!r} is not a lower-case label of 1 to 32 characters")
    if "clusterID" in r and not (is_int(r["clusterID"]) and 1 <= r["clusterID"] <= 255):
        out.append(f"document-shape: {where}.clusterID {r['clusterID']!r} is not an integer from 1 to 255")
    if "nodeCIDR" in r and ipv4_net(r["nodeCIDR"]) is None:
        out.append(f"document-shape: {where}.nodeCIDR {r['nodeCIDR']!r} is not an IPv4 CIDR")
    for name in ("podCIDR", "serviceCIDR"):
        if name in r:
            n = ipv4_net(r[name])
            if n is None or n.prefixlen != 16:
                out.append(f"document-shape: {where}.{name} {r[name]!r} is not an IPv4 CIDR of prefix length 16")
    if "edgeAddress" in r and not ipv4_addr(r["edgeAddress"]):
        out.append(f"document-shape: {where}.edgeAddress {r['edgeAddress']!r} is not an IPv4 address")
    if "egressAddresses" in r:
        v = r["egressAddresses"]
        if not (isinstance(v, list) and v and all(ipv4_addr(a) for a in v)):
            out.append(f"document-shape: {where}.egressAddresses is not a list of at least one IPv4 address")
    if "meshEndpoint" in r:
        m = r["meshEndpoint"]
        if not isinstance(m, dict):
            out.append(f"document-shape: {where}.meshEndpoint is not an object")
        else:
            check_keys(f"{where}.meshEndpoint", m, MESH_KEYS, set(), out)
            ips = m.get("ips")
            if "ips" in m and not (isinstance(ips, list) and ips and all(ipv4_addr(a) for a in ips)):
                out.append(f"document-shape: {where}.meshEndpoint.ips is not a list of at least one IPv4 address")
            port = m.get("port")
            if "port" in m and not (is_int(port) and 1 <= port <= 65535):
                out.append(f"document-shape: {where}.meshEndpoint.port {port!r} is not a port number")
    return out


def check_rules(doc):
    """Return the violations of the rules after document-shape."""
    out = []
    regions = doc["regions"]
    supernet = ipv4_net(doc["podSupernet"])

    for field, rule in (("clusterID", "unique-cluster-id"), ("clusterName", "unique-cluster-name")):
        seen = {}
        for r in regions:
            v = r[field]
            if v in seen:
                out.append(f"{rule}: regions {seen[v]} and {r['code']} share {field} {v!r}")
            else:
                seen[v] = r["code"]

    ranges = [(f"{r['code']}.{f}", ipv4_net(r[f])) for r in regions for f in RANGE_FIELDS]
    for i, (name_a, a) in enumerate(ranges):
        for name_b, b in ranges[i + 1:]:
            if a.overlaps(b):
                out.append(f"unique-ranges: {name_a} {a} overlaps {name_b} {b}")

    for r in regions:
        pod = ipv4_net(r["podCIDR"])
        if not pod.subnet_of(supernet):
            out.append(f"supernet-covers-pods: {r['code']}.podCIDR {pod} is not inside podSupernet {supernet}")
        for f in ("nodeCIDR", "serviceCIDR"):
            n = ipv4_net(r[f])
            if n.overlaps(supernet):
                out.append(f"supernet-excludes-nodes-and-services: {r['code']}.{f} {n} intersects podSupernet {supernet}")

    if len(regions) > 1:
        for r in regions:
            if "meshEndpoint" not in r:
                out.append(f"mesh-endpoint-when-multi-region: region {r['code']} has no meshEndpoint and the document has {len(regions)} regions")

    for name in sorted(ACCESS_KEYS):
        for c in doc["controlPlaneAccess"][name]:
            if any_net(c).prefixlen == 0:
                out.append(f"no-any-source: controlPlaneAccess.{name} holds {c}")
    return out


def check(doc):
    """Return every violation of the document, one line each."""
    shape = check_shape(doc)
    if shape:
        return shape
    return check_rules(doc)


def load(path):
    """Read a document. A JSON string that holds the object is decoded once more."""
    doc = json.loads(pathlib.Path(path).read_text(encoding="utf-8"))
    if isinstance(doc, str):
        doc = json.loads(doc)
    return doc


def self_test():
    failed = 0
    valid = sorted(SELF_TEST_DIR.glob("valid-*.json"))
    if not valid:
        print(f"SELF-TEST FAIL: no valid-*.json under {SELF_TEST_DIR}")
        return 1
    for f in valid:
        lines = check(load(f))
        if lines:
            failed += 1
            print(f"SELF-TEST FAIL: {f.name} must pass, got: {lines}")
        else:
            print(f"ok   {f.name} passes")
    for rule in RULES:
        f = SELF_TEST_DIR / "invalid" / f"{rule}.json"
        if not f.is_file():
            failed += 1
            print(f"SELF-TEST FAIL: no case for rule {rule} ({f})")
            continue
        lines = check(load(f))
        if any(line.startswith(rule + ": ") for line in lines):
            print(f"ok   invalid/{f.name} fails with {rule}")
        else:
            failed += 1
            print(f"SELF-TEST FAIL: invalid/{f.name} must fail with {rule}, got: {lines}")
    extra = sorted(p.name for p in (SELF_TEST_DIR / "invalid").glob("*.json") if p.stem not in RULES)
    for name in extra:
        failed += 1
        print(f"SELF-TEST FAIL: invalid/{name} names no rule")
    if failed:
        print(f"network-registry self-test: FAILED ({failed})")
        return 1
    print("network-registry self-test: ok")
    return 0


def main(argv):
    if argv == ["--self-test"]:
        return self_test()
    if len(argv) != 1 or argv[0].startswith("-"):
        print("usage: check-network-registry.py <file> | --self-test", file=sys.stderr)
        return 2
    try:
        doc = load(argv[0])
    except (OSError, ValueError) as e:
        print(f"cannot read {argv[0]}: {e}", file=sys.stderr)
        return 2
    lines = check(doc)
    for line in lines:
        print(line)
    if lines:
        return 1
    print(f"OK: {argv[0]} is a valid network registry")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
