#!/usr/bin/env python3
"""providers.py - the tables of the provider packs and their cloud profiles, generated from the tree.

It reads   infra/providers/<pack>/pack.yaml      (the pack declaration)
           infra/providers/<pack>/profile.yaml   (the cloud profile)
           for every folder <pack> directly under infra/providers whose name starts
           with neither "_" nor "."
It writes  docs/generated/providers.md

Usage, from any directory:
  python3 scripts/generators/providers.py              write the file
  python3 scripts/generators/providers.py --check      write nothing; exit 1 when the file on disk differs
  python3 scripts/generators/providers.py --self-test  prove every rule on a throw-away tree

Exit codes: 0 done or current; 1 the file is stale, or a self-test case failed;
2 an input is missing or cannot be read.

The rules of an input:
  - a pack folder holds both files; each is a YAML mapping with the key spec;
  - kind is ProviderPack in pack.yaml and CloudProfile in profile.yaml;
  - metadata.name equals the name of the folder;
  - every field in the lists PACK_FIELDS and PROFILE_FIELDS below is present;
  - a field of the list LISTS below that is present is a list;
  - an entry of spec.tofuProviders has a source, a credential field has a key, a region has
    a code and a flavor has a name;
  - spec.compute.defaults and spec.compute.minimumFlavor, when they are set, are mappings;
  - at least one pack folder exists.
A shown field that is in neither list and is missing is no error: it reads "not set", or a
dash when it is a list.
The schemas in infra/providers/_contract/ judge everything else; this generator does not
repeat them. A field it does not show is ignored, so a new schema field never makes it fail.

This file names no cloud: the provider-leak scanner reads it as shared code.
"""
import difflib
import os
import sys
import tempfile

try:
    import yaml
except ImportError:
    sys.stderr.write("providers.py needs PyYAML: pip install pyyaml\n")
    sys.exit(2)

SCRIPT = "scripts/generators/providers.py"
OUTPUT = "docs/generated/providers.md"
PACKS = "infra/providers"
FROM_INSTALLATION = "from the installation"
NOT_SET = "not set"

# Fields that must be present, as paths below the top of the file.
PACK_FIELDS = ("spec.contractVersion", "spec.module", "spec.overlay", "spec.adapter", "spec.tofuProviders")
PROFILE_FIELDS = ("spec.displayName", "spec.lifecycle", "spec.contractVersion", "spec.credentials.source",
                  "spec.credentials.fields", "spec.regions", "spec.compute.image", "spec.compute.flavors",
                  "spec.storage.defaultClass", "spec.storage.driver", "spec.network.loadBalancer.mode",
                  "spec.capabilities.multiRegion", "spec.capabilities.multiCloudRegion")
LISTS = ("spec.tofuProviders", "spec.charts", "spec.leakTokens", "spec.credentials.fields", "spec.regions",
         "spec.compute.flavors", "spec.day2.contracts")


class InputError(Exception):
    """An input is missing or does not have the shape this generator reads."""


def cell(text):
    return str(text).replace("|", "\\|").replace("\n", " ")


def code(text):
    return "`%s`" % str(text).replace("`", "'").replace("|", "\\|").replace("\n", " ")


def number(value):
    if isinstance(value, float) and value.is_integer():
        return str(int(value))
    return str(value)


def show(value):
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, (int, float)):
        return number(value)
    return str(value)


MISSING = object()


def lookup(document, path):
    """The value at a dotted path below the top of a document, or MISSING."""
    value = document
    for key in path.split("."):
        if not isinstance(value, dict) or key not in value:
            return MISSING
        value = value[key]
    return value


def load(root, relative, kind, name, required):
    path = os.path.join(root, relative)
    try:
        with open(path, encoding="utf-8") as handle:
            document = yaml.safe_load(handle)
    except OSError as error:
        raise InputError("%s: cannot be read: %s" % (relative, error.strerror))
    except yaml.YAMLError as error:
        raise InputError("%s: is not valid YAML: %s" % (relative, str(error).splitlines()[0]))
    if not isinstance(document, dict) or not isinstance(document.get("spec"), dict):
        raise InputError("%s: is not a mapping with the key spec" % relative)
    if document.get("kind") != kind:
        raise InputError("%s: kind is not %s" % (relative, kind))
    if lookup(document, "metadata.name") != name:
        raise InputError("%s: metadata.name is not %s, the name of its folder" % (relative, name))
    for field in required:
        if lookup(document, field) is MISSING:
            raise InputError("%s: has no field %s" % (relative, field))
    for field in LISTS:
        value = lookup(document, field)
        if value is not MISSING and not isinstance(value, list):
            raise InputError("%s: %s is not a list" % (relative, field))
    return document


def read_packs(root):
    folder = os.path.join(root, PACKS)
    try:
        names = sorted(name for name in os.listdir(folder)
                       if os.path.isdir(os.path.join(folder, name)) and not name.startswith(("_", ".")))
    except OSError as error:
        raise InputError("%s: cannot be listed: %s" % (PACKS, error.strerror))
    if not names:
        raise InputError("%s: has no pack folder" % PACKS)
    packs = []
    for name in names:
        pack = load(root, "%s/%s/pack.yaml" % (PACKS, name), "ProviderPack", name, PACK_FIELDS)
        profile = load(root, "%s/%s/profile.yaml" % (PACKS, name), "CloudProfile", name, PROFILE_FIELDS)
        packs.append((name, pack, profile))
    return packs


def listed(values):
    """A list as code words joined by commas, or a dash when it is empty or not set."""
    if values is MISSING or not values:
        return "-"
    return ", ".join(code(show(v)) for v in values)


def scalar(value):
    """A single value as code, `-` when it is empty, `not set` when the file does not set it."""
    if value is MISSING or value is None:
        return NOT_SET
    if value == "":
        return "-"
    return code(show(value))


def needs_installation(profile):
    return lookup(profile, "spec.requiresInstallation") is True


def count_or_installation(profile, path):
    values = lookup(profile, path)
    if not values and needs_installation(profile):
        return FROM_INSTALLATION
    return str(len(values))


def credential_fields(fields):
    shown = []
    for field in fields:
        if not isinstance(field, dict) or "key" not in field:
            raise InputError("a credential field has no key")
        marks = ["secret" if field.get("secret") is True else "not secret",
                 "required" if field.get("required") is True else "optional"]
        shown.append("%s (%s)" % (code(field["key"]), ", ".join(marks)))
    return ", ".join(shown) if shown else "-"


def defaults(value):
    if value is MISSING or value is None:
        return NOT_SET
    if not isinstance(value, dict):
        raise InputError("spec.compute.defaults is not a mapping")
    return "control plane %s, worker %s" % (scalar(value.get("controlPlaneFlavor", MISSING)),
                                             scalar(value.get("workerFlavor", MISSING)))


def size(value):
    if not isinstance(value, dict):
        return NOT_SET
    return "%s vCPU, %s GiB" % (show(value.get("vcpu", "?")), show(value.get("memoryGiB", "?")))


def minimum_flavor(value):
    if value is MISSING or value is None:
        return NOT_SET
    if not isinstance(value, dict):
        raise InputError("spec.compute.minimumFlavor is not a mapping")
    return "control plane %s; worker %s" % (size(value.get("controlPlane")), size(value.get("worker")))


def image(profile):
    value = lookup(profile, "spec.compute.image")
    if value == "" and needs_installation(profile):
        return FROM_INSTALLATION
    return scalar(value)


def price(value):
    if not isinstance(value, dict):
        return "-"
    return "%s %s per %s" % (number(value.get("amount", "?")), cell(value.get("currency", "?")),
                             cell(value.get("per", "?")))


def generate(root):
    packs = read_packs(root)
    lines = [
        "Generated by %s. Do not edit." % SCRIPT,
        "",
        "# Provider packs",
        "",
        "The provider packs of this repository and their cloud profiles.",
        "A pack is a folder `%s/<name>/` whose name does not start with `_`." % PACKS,
        "It holds the pack declaration `pack.yaml` and the cloud profile `profile.yaml`.",
        "Their schemas are `%s/_contract/pack.schema.json` and `%s/_contract/cloudprofile.schema.json`." % (PACKS, PACKS),
        "To change this file, change a `pack.yaml` or a `profile.yaml` and run `python3 %s`." % SCRIPT,
        "The check `generated-docs` fails a pull request in which this file is stale.",
        "",
        "- A pack profile holds no value of one installation. When a profile sets `requiresInstallation: true`,"
        " an installation document that is kept outside this repository supplies its regions, its image and"
        " its flavors. Such a value that the pack profile leaves empty reads `%s`." % FROM_INSTALLATION,
        "- `%s`: the file does not set the field; the schema gives its default." % NOT_SET,
        "- A dash means an empty text, or a list that is empty or not set.",
        "",
        "## Packs",
        "",
        "One row for each pack, in the order of the folder names.",
        "",
        "| Pack | Cloud | Lifecycle | Installation document | Regions | Flavors | Module | Overlay | Adapter |",
        "|---|---|---|---|---|---|---|---|---|",
    ]
    for name, pack, profile in packs:
        lines.append("| %s | %s | %s | %s | %s | %s | %s | %s | %s |" % (
            cell(name), cell(lookup(profile, "spec.displayName")), scalar(lookup(profile, "spec.lifecycle")),
            "required" if needs_installation(profile) else "not required",
            count_or_installation(profile, "spec.regions"), count_or_installation(profile, "spec.compute.flavors"),
            scalar(lookup(pack, "spec.module")), scalar(lookup(pack, "spec.overlay")),
            scalar(lookup(pack, "spec.adapter"))))

    for name, pack, profile in packs:
        tofu = []
        for entry in lookup(pack, "spec.tofuProviders"):
            if not isinstance(entry, dict) or "source" not in entry:
                raise InputError("%s/%s/pack.yaml: an entry of spec.tofuProviders has no source" % (PACKS, name))
            tofu.append(entry["source"])
        try:
            fields = credential_fields(lookup(profile, "spec.credentials.fields"))
            compute_defaults = defaults(lookup(profile, "spec.compute.defaults"))
            floor = minimum_flavor(lookup(profile, "spec.compute.minimumFlavor"))
        except InputError as error:
            raise InputError("%s/%s/profile.yaml: %s" % (PACKS, name, error))
        rows = [
            ("pack.yaml", "spec.contractVersion", scalar(lookup(pack, "spec.contractVersion"))),
            ("pack.yaml", "spec.charts", listed(lookup(pack, "spec.charts"))),
            ("pack.yaml", "spec.tofuProviders", listed(tofu)),
            ("pack.yaml", "spec.leakTokens", listed(lookup(pack, "spec.leakTokens"))),
            ("profile.yaml", "spec.contractVersion", scalar(lookup(profile, "spec.contractVersion"))),
            ("profile.yaml", "spec.requiresInstallation", scalar(lookup(profile, "spec.requiresInstallation"))),
            ("profile.yaml", "spec.credentials.source", scalar(lookup(profile, "spec.credentials.source"))),
            ("profile.yaml", "spec.credentials.fields", fields),
            ("profile.yaml", "spec.compute.image", image(profile)),
            ("profile.yaml", "spec.compute.defaults", compute_defaults),
            ("profile.yaml", "spec.compute.minWorkerCount", scalar(lookup(profile, "spec.compute.minWorkerCount"))),
            ("profile.yaml", "spec.compute.minimumFlavor", floor),
            ("profile.yaml", "spec.storage.defaultClass", scalar(lookup(profile, "spec.storage.defaultClass"))),
            ("profile.yaml", "spec.storage.driver", scalar(lookup(profile, "spec.storage.driver"))),
            ("profile.yaml", "spec.objectStorage.checksumAlgorithm",
             scalar(lookup(profile, "spec.objectStorage.checksumAlgorithm"))),
            ("profile.yaml", "spec.network.loadBalancer.mode", scalar(lookup(profile, "spec.network.loadBalancer.mode"))),
            ("profile.yaml", "spec.seal.kind", scalar(lookup(profile, "spec.seal.kind"))),
            ("profile.yaml", "spec.bootstrap.userDataLimitBytes",
             scalar(lookup(profile, "spec.bootstrap.userDataLimitBytes"))),
            ("profile.yaml", "spec.capabilities.multiRegion", scalar(lookup(profile, "spec.capabilities.multiRegion"))),
            ("profile.yaml", "spec.capabilities.multiCloudRegion",
             scalar(lookup(profile, "spec.capabilities.multiCloudRegion"))),
            ("profile.yaml", "spec.day2.contracts", listed(lookup(profile, "spec.day2.contracts"))),
        ]
        lines += [
            "",
            "## Pack %s" % name,
            "",
            "| File | Field | Value |",
            "|---|---|---|",
        ]
        for source, field, value in rows:
            lines.append("| %s | %s | %s |" % (source, field, value))

    lines += [
        "",
        "## Regions",
        "",
        "One row for each region of each pack profile, in the order of the profile.",
        "A pack whose profile lists no region has no row here; see the column Regions of the table Packs.",
        "",
    ]
    rows = []
    for name, pack, profile in packs:
        for region in lookup(profile, "spec.regions"):
            if not isinstance(region, dict) or "code" not in region:
                raise InputError("%s/%s/profile.yaml: a region has no code" % (PACKS, name))
            zones = region.get("zones")
            rows.append("| %s | %s | %s | %s | %s | %s |" % (
                cell(name), code(region["code"]), cell(region.get("label", "-")), cell(region.get("location", "-")),
                listed(zones if isinstance(zones, list) else MISSING), scalar(region.get("defaultZone", MISSING))))
    if rows:
        lines += ["| Pack | Region | Label | Location | Zones | Default zone |", "|---|---|---|---|---|---|"] + rows
    else:
        lines.append("None.")

    lines += [
        "",
        "## Flavors",
        "",
        "One row for each flavor of each pack profile, in the order of the profile.",
        "A pack whose profile lists no flavor has no row here; see the column Flavors of the table Packs.",
        "Regions: the regions where the flavor can be ordered; `every region` when the profile names none.",
        "Price: for display only; the billing engine keeps the price book.",
        "",
    ]
    rows = []
    for name, pack, profile in packs:
        for flavor in lookup(profile, "spec.compute.flavors"):
            if not isinstance(flavor, dict) or "name" not in flavor:
                raise InputError("%s/%s/profile.yaml: a flavor has no name" % (PACKS, name))
            regions = flavor.get("regions")
            rows.append("| %s | %s | %s | %s | %s | %s | %s |" % (
                cell(name), code(flavor["name"]), cell(show(flavor.get("vcpu", "-"))),
                cell(show(flavor.get("memoryGiB", "-"))), listed(flavor.get("roles", MISSING)),
                listed(regions) if regions else "every region", price(flavor.get("price"))))
    if rows:
        lines += ["| Pack | Flavor | vCPU | Memory GiB | Roles | Regions | Price |",
                  "|---|---|---|---|---|---|---|"] + rows
    else:
        lines.append("None.")
    return "\n".join(lines) + "\n"


def write(root):
    text = generate(root)
    path = os.path.join(root, OUTPUT)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8", newline="\n") as handle:
        handle.write(text)
    print("wrote: %s" % OUTPUT)
    return 0


def check(root):
    text = generate(root)
    try:
        with open(os.path.join(root, OUTPUT), encoding="utf-8", newline="") as handle:
            on_disk = handle.read()
    except OSError:
        on_disk = None
    if on_disk == text:
        print("current: %s" % OUTPUT)
        return 0
    if on_disk is None:
        print("%s does not exist" % OUTPUT)
    else:
        diff = list(difflib.unified_diff(on_disk.splitlines(), text.splitlines(), "on disk", "generated", lineterm="", n=0))
        for line in diff[:30]:
            print(line[:240])
        if len(diff) > 30:
            print("... %d more lines of difference" % (len(diff) - 30))
    print("stale: %s. Run: python3 %s  and commit that file." % (OUTPUT, SCRIPT))
    return 1


# ---------------------------------------------------------------------------
# Self-test: every rule is shown on a throw-away tree, and the check is shown to fail.

PACK = """apiVersion: catalyst.openova.io/v1alpha1
kind: ProviderPack
metadata:
  name: %(name)s
spec:
  contractVersion: "1"
  module: infra/providers/%(name)s
  overlay: clusters/_template/providers/%(name)s
  adapter: products/catalyst/bootstrap/api/internal/providers/%(name)s
%(charts)s  tofuProviders:
    - source: registry.opentofu.org/example/%(name)s
  leakTokens: [%(name)s]
"""

ALPHA = """apiVersion: catalyst.openova.io/v1alpha1
kind: CloudProfile
metadata:
  name: alpha
spec:
  displayName: Alpha Cloud
  lifecycle: proven
  contractVersion: "1"
  credentials:
    source: user
    fields:
      - {key: token, label: Token, secret: true, required: true, pattern: "^(a|b)$"}
      - {key: project, label: Project, secret: false, required: false}
  regions:
    - {code: north-1, label: North, location: "Town, Land", zones: [north-1a, north-1b], defaultZone: north-1a}
    - {code: south-1, label: South, location: Port, zones: [south-1a], defaultZone: south-1a}
  compute:
    image: base-24
    flavors:
      - {name: small, vcpu: 2, memoryGiB: 4.0, roles: [control-plane, worker], regions: [], price: {amount: 3.5, currency: EUR, per: month}}
      - {name: large, vcpu: 8, memoryGiB: 0.5, roles: [worker], regions: [north-1]}
    defaults: {controlPlaneFlavor: small, workerFlavor: large}
    minWorkerCount: 0
  storage: {defaultClass: alpha-disk, driver: disk.alpha.example}
  objectStorage: {checksumAlgorithm: CRC32}
  network:
    loadBalancer: {mode: controller}
  bootstrap: {userDataLimitBytes: 32768}
  capabilities: {multiRegion: false, multiCloudRegion: true}
  day2: {contracts: []}
  futureField: {anything: 1}
"""

BETA = """apiVersion: catalyst.openova.io/v1alpha1
kind: CloudProfile
metadata:
  name: beta
spec:
  displayName: Beta Stack
  lifecycle: unproven
  contractVersion: "1"
  requiresInstallation: true
  credentials:
    source: either
    fields:
      - {key: access, label: Access, secret: false, required: true}
  regions: []
  compute:
    image: ""
    flavors: []
    minimumFlavor:
      controlPlane: {vcpu: 4, memoryGiB: 32}
      worker: {vcpu: 8, memoryGiB: 64}
  storage: {defaultClass: beta-disk, driver: disk.beta.example}
  objectStorage: {checksumAlgorithm: ""}
  network:
    loadBalancer: {mode: external-hostport}
  seal: {kind: static-node-file}
  bootstrap: {userDataLimitBytes: 0}
  capabilities: {multiRegion: true, multiCloudRegion: false}
  day2: {contracts: [NodePool]}
"""


def put(root, relative, text):
    path = os.path.join(root, relative)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8") as handle:
        handle.write(text)


def pack_text(name, charts=""):
    return PACK % {"name": name, "charts": charts}


def fixture(root):
    put(root, PACKS + "/alpha/pack.yaml", pack_text("alpha", "  charts: [alpha-ccm, alpha-csi]\n"))
    put(root, PACKS + "/alpha/profile.yaml", ALPHA)
    put(root, PACKS + "/beta/pack.yaml", pack_text("beta"))
    put(root, PACKS + "/beta/profile.yaml", BETA)
    put(root, PACKS + "/_contract/pack.schema.json", "{}\n")
    put(root, PACKS + "/_shared/notes.txt", "not a pack\n")
    put(root, PACKS + "/.hidden/pack.yaml", "not a pack\n")
    put(root, PACKS + "/INTERFACE.md", "not a pack\n")


def self_test():
    failures = []

    def case(name, ok):
        print("%s  %s" % ("ok  " if ok else "FAIL", name))
        if not ok:
            failures.append(name)

    def input_error(root, words):
        try:
            generate(root)
        except InputError as error:
            return words in str(error)
        return False

    with tempfile.TemporaryDirectory() as root:
        fixture(root)
        text = generate(root)
        lines = text.splitlines()
        case("the first line says who generates the file",
             lines[0] == "Generated by scripts/generators/providers.py. Do not edit.")
        case("a pack row carries the cloud, the lifecycle, the counts and the three roots",
             "| alpha | Alpha Cloud | `proven` | not required | 2 | 2 | `infra/providers/alpha` | "
             "`clusters/_template/providers/alpha` | `products/catalyst/bootstrap/api/internal/providers/alpha` |" in text)
        case("a pack that requires an installation shows its empty regions and flavors as from the installation",
             "| beta | Beta Stack | `unproven` | required | from the installation | from the installation |" in text)
        case("packs follow the order of the folder names",
             text.index("| alpha | Alpha Cloud |") < text.index("| beta | Beta Stack |")
             and text.index("## Pack alpha") < text.index("## Pack beta"))
        case("folders that start with _ or ., and plain files, are not packs",
             "_contract" not in text.split("## Packs")[1] and "_shared" not in text
             and ".hidden" not in text and "INTERFACE" not in text)
        case("lists show as code words; an empty list and an empty text as a dash",
             "| pack.yaml | spec.charts | `alpha-ccm`, `alpha-csi` |" in text
             and "| pack.yaml | spec.charts | - |" in text
             and "| profile.yaml | spec.objectStorage.checksumAlgorithm | - |" in text
             and "| profile.yaml | spec.day2.contracts | - |" in text)
        case("a field the file does not set reads not set",
             "| profile.yaml | spec.requiresInstallation | not set |" in text
             and "| profile.yaml | spec.seal.kind | not set |" in text
             and "| profile.yaml | spec.compute.defaults | not set |" in text)
        case("credential fields show their key and marks, and no pattern",
             "| profile.yaml | spec.credentials.fields | `token` (secret, required), `project` (not secret, optional) |" in text
             and "(a|b)" not in text and "(a\\|b)" not in text)
        case("an empty image of a pack that requires an installation reads from the installation",
             "| profile.yaml | spec.compute.image | from the installation |" in text
             and "| profile.yaml | spec.compute.image | `base-24` |" in text)
        case("defaults, the minimum flavor, booleans and numbers are shown",
             "| profile.yaml | spec.compute.defaults | control plane `small`, worker `large` |" in text
             and "| profile.yaml | spec.compute.minimumFlavor | control plane 4 vCPU, 32 GiB; worker 8 vCPU, 64 GiB |" in text
             and "| profile.yaml | spec.capabilities.multiCloudRegion | `true` |" in text
             and "| profile.yaml | spec.bootstrap.userDataLimitBytes | `0` |" in text
             and "| profile.yaml | spec.compute.minWorkerCount | `0` |" in text)
        case("a region row carries its zones and default zone",
             "| alpha | `north-1` | North | Town, Land | `north-1a`, `north-1b` | `north-1a` |" in text)
        case("a flavor row: an empty region list reads every region; a whole float has no decimals; a price is shown",
             "| alpha | `small` | 2 | 4 | `control-plane`, `worker` | every region | 3.5 EUR per month |" in text
             and "| alpha | `large` | 8 | 0.5 | `worker` | `north-1` | - |" in text)
        case("a field this generator does not show is ignored", "futureField" not in text and "anything" not in text)
        case("one pack row, one field table and its rows per pack; region and flavor rows",
             sum(1 for line in lines if line.startswith("| pack.yaml |")) == 8
             and sum(1 for line in lines if line.startswith("| profile.yaml |")) == 34
             and sum(1 for line in lines if line.startswith("| alpha | `")) == 4)

        write(root)
        case("--check passes on a fresh output", check(root) == 0)

        put(root, PACKS + "/alpha/profile.yaml", ALPHA.replace("lifecycle: proven", "lifecycle: retired"))
        changed = generate(root)
        case("a changed profile changes the file", "| alpha | Alpha Cloud | `retired` |" in changed)
        case("--check fails when the file on disk is stale", check(root) == 1)

        put(root, PACKS + "/alpha/profile.yaml", ALPHA)
        put(root, PACKS + "/alpha/pack.yaml", pack_text("alpha"))
        case("a changed pack declaration changes the file and --check fails", check(root) == 1)

        put(root, PACKS + "/alpha/pack.yaml", pack_text("alpha", "  charts: [alpha-ccm, alpha-csi]\n"))
        case("--check passes again when the inputs are as before", check(root) == 0)

        os.remove(os.path.join(root, OUTPUT))
        case("--check fails when the file does not exist", check(root) == 1)

        put(root, PACKS + "/gamma/pack.yaml", pack_text("gamma"))
        case("a pack folder without profile.yaml is an input error", input_error(root, "gamma/profile.yaml: cannot be read"))
        os.remove(os.path.join(root, PACKS, "gamma", "pack.yaml"))
        put(root, PACKS + "/gamma/profile.yaml", BETA.replace("name: beta", "name: gamma"))
        case("a pack folder without pack.yaml is an input error", input_error(root, "gamma/pack.yaml: cannot be read"))
        put(root, PACKS + "/gamma/pack.yaml", pack_text("gamma"))
        case("a third pack is read", "| gamma | Beta Stack |" in generate(root))

        put(root, PACKS + "/gamma/pack.yaml", pack_text("gamma").replace("name: gamma", "name: delta"))
        case("metadata.name that is not the folder name is an input error", input_error(root, "metadata.name is not gamma"))
        put(root, PACKS + "/gamma/pack.yaml", pack_text("gamma").replace("kind: ProviderPack", "kind: CloudProfile"))
        case("a wrong kind is an input error", input_error(root, "kind is not ProviderPack"))
        put(root, PACKS + "/gamma/pack.yaml", pack_text("gamma").replace("  module: infra/providers/gamma\n", ""))
        case("a missing required field is an input error", input_error(root, "has no field spec.module"))
        put(root, PACKS + "/gamma/pack.yaml",
            pack_text("gamma").replace("- source: registry.opentofu.org/example/gamma", "- name: gamma"))
        case("an entry of spec.tofuProviders without a source is an input error",
             input_error(root, "gamma/pack.yaml: an entry of spec.tofuProviders has no source"))
        put(root, PACKS + "/gamma/pack.yaml", pack_text("gamma"))
        gamma = BETA.replace("name: beta", "name: gamma")
        floor = "    minimumFlavor:\n      controlPlane: {vcpu: 4, memoryGiB: 32}\n      worker: {vcpu: 8, memoryGiB: 64}\n"
        for name, old, new, words in (
                ("a credential field without a key", "{key: access, ", "{", "a credential field has no key"),
                ("a region without a code", "regions: []", "regions: [{label: North}]", "a region has no code"),
                ("a flavor without a name", "flavors: []", "flavors: [{vcpu: 2}]", "a flavor has no name"),
                ("spec.compute.defaults that is not a mapping", '    image: ""\n', '    image: ""\n    defaults: small\n',
                 "spec.compute.defaults is not a mapping"),
                ("spec.compute.minimumFlavor that is not a mapping", floor, "    minimumFlavor: small\n",
                 "spec.compute.minimumFlavor is not a mapping")):
            put(root, PACKS + "/gamma/profile.yaml", gamma.replace(old, new))
            case("%s is an input error" % name, old in gamma and input_error(root, "gamma/profile.yaml: " + words))
        put(root, PACKS + "/gamma/profile.yaml", gamma.replace("regions: []", "regions: north"))
        case("a list field that is not a list is an input error", input_error(root, "spec.regions is not a list"))
        put(root, PACKS + "/gamma/profile.yaml", "kind: CloudProfile\nspec: [broken\n")
        case("a file that is not valid YAML is an input error", input_error(root, "is not valid YAML"))
        put(root, PACKS + "/gamma/profile.yaml", "- a list\n")
        case("a file that is not a mapping with spec is an input error", input_error(root, "is not a mapping with the key spec"))

    with tempfile.TemporaryDirectory() as root:
        os.makedirs(os.path.join(root, PACKS, "_contract"))
        case("a tree with no pack folder is an input error", input_error(root, "has no pack folder"))
    with tempfile.TemporaryDirectory() as root:
        case("a tree without infra/providers is an input error", input_error(root, "cannot be listed"))

    if failures:
        print("self-test: %d case(s) FAILED" % len(failures))
        return 1
    print("self-test: all cases passed")
    return 0


def main(argv):
    root = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
    if argv == ["--self-test"]:
        return self_test()
    if argv not in ([], ["--check"]):
        sys.stderr.write("usage: python3 %s [--check | --self-test]\n" % SCRIPT)
        return 2
    try:
        return check(root) if argv else write(root)
    except InputError as error:
        sys.stderr.write("%s: %s\n" % (SCRIPT, error))
        return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
