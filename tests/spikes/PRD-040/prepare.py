#!/usr/bin/env python3
"""Prepare the Keycloak FAPI SIG conformance environment for lab test PRD-040.

Reads the SIG checkout at $KCSIG and changes only that checkout:
  * realm   realms/realm-prd040.json, built from the SIG's UK-OB realm and its FAPI1 PAR clients
  * suite   configs under fapi-conformance-suite-configs/prd040/ for the three plan groups
  * images  Keycloak image gets the stub extension jar; resource server replaced by this stub;
            the suite build removes one variant guard (FAPI1 + openbanking_uk + PAR)
  * metadata override: the SIG's file without its authorization_details line
"""
import copy
import json
import os
import pathlib
import re
import shutil
import sys

HERE = pathlib.Path(__file__).resolve().parent
KCSIG = pathlib.Path(os.environ["KCSIG"]) / "conformance-tests-env"
KC_VERSION = os.environ["KC_VERSION"]
KC_DIR = KCSIG / "test-target/keycloak/keycloak"
REALMS = KC_DIR / "realms"
CFG = KCSIG / "conformance-suite/fapi-conformance-suite-configs"
SUITE_HOST = "https://conformance-suite.keycloak-fapi.org"
OB_BASE = "https://rs.keycloak-fapi.org/open-banking/v4.0/aisp/"
CLAIM = "openbanking_intent_id"
ALIAS = {"p1": "prd040-p1", "p2": "prd040-p2", "p3": "prd040-p3"}
RS_ALIAS = "prd040-resource-server"
PAR_SUFFIX = "-fapi1-advanced-par"
UKPAR_SUFFIX = "-fapi-uk-ob-par"


def die(msg):
    print("prepare: " + msg, file=sys.stderr)
    sys.exit(1)


def version_tuple(v):
    return tuple(int(x) for x in re.findall(r"\d+", v)[:3])


def load(path):
    with open(path) as f:
        return json.load(f)


def dump(path, obj):
    path.parent.mkdir(parents=True, exist_ok=True)
    with open(path, "w") as f:
        json.dump(obj, f, indent=2)
        f.write("\n")


def strip_ids(client):
    client.pop("id", None)
    for m in client.get("protocolMappers", []):
        m.pop("id", None)
    return client


def add_redirects(client, alias):
    base = f"{SUITE_HOST}/test/a/{alias}/callback"
    uris = client.setdefault("redirectUris", [])
    for u in (base, base + "?dummy1=lorem&dummy2=ipsum"):
        if u not in uris:
            uris.append(u)


def stub_mapper():
    return {
        "name": "prd040 consent claim",
        "protocol": "openid-connect",
        "protocolMapper": "prd040-consent-claim-mapper",
        "consentRequired": False,
        "config": {
            "claim.name": CLAIM,
            "consent.step.url": f"http://{RS_ALIAS}:3000/internal/consent-step",
            "access.token.claim": "true",
            "id.token.claim": "true",
            "userinfo.token.claim": "false",
            "introspection.token.claim": "true",
        },
    }


def build_realm():
    uk = load(REALMS / "realm-fapi-uk-ob.json")
    adv = load(REALMS / "realm-fapi1-advanced.json")
    uk_clients = [c for c in uk["clients"] if c["clientId"].endswith("-fapi-uk-ob")]
    if len(uk_clients) != 4:
        die(f"expected 4 UK-OB clients, found {len(uk_clients)}")
    ob_acr_map = uk_clients[0]["attributes"]["acr.loa.map"]
    ob_default_scopes = uk_clients[0]["defaultClientScopes"]
    ob_optional_scopes = uk_clients[0]["optionalClientScopes"]

    # Group P1: the SIG's UK-OB clients (request object by value). The built-in
    # claims-parameter value mapper is replaced by the stub extension, so the
    # consent claim reaches the tokens only through the extension.
    for c in uk_clients:
        before = len(c["protocolMappers"])
        c["protocolMappers"] = [m for m in c["protocolMappers"]
                                if m["protocolMapper"] != "oidc-claims-param-value-idtoken-mapper"]
        if len(c["protocolMappers"]) != before - 1:
            die("built-in value mapper not found on " + c["clientId"])
        c["protocolMappers"].append(stub_mapper())
        add_redirects(c, ALIAS["p1"])

    # Group P2: the SIG's FAPI1 Advanced PAR clients, PS256, both client authentications.
    par_ids = [f"client{n}-{auth}-PS256-PS256{PAR_SUFFIX}" for auth in ("private_key_jwt", "mtls") for n in (1, 2)]
    par_clients = []
    for cid in par_ids:
        found = [c for c in adv["clients"] if c["clientId"] == cid]
        if len(found) != 1:
            die("PAR client missing in SIG realm: " + cid)
        c = strip_ids(copy.deepcopy(found[0]))
        c["defaultClientScopes"] = list(ob_default_scopes)
        c["optionalClientScopes"] = list(ob_optional_scopes)
        add_redirects(c, ALIAS["p2"])
        par_clients.append(c)
        roles = copy.deepcopy(adv["roles"]["client"][cid])
        for r in roles:
            r.pop("containerId", None)
        uk["roles"]["client"][cid] = roles
    uk["clients"].extend(par_clients)
    par_policy = [p for p in adv["clientPolicies"]["policies"] if p["name"] == "fapi1-advanced-par-policy"]
    if len(par_policy) != 1:
        die("fapi1-advanced-par-policy missing in SIG realm")
    uk["clientPolicies"]["policies"].append(copy.deepcopy(par_policy[0]))

    # Group P3: the same PAR clients again, now with the consent carrier: UK-OB
    # acr values, client credentials for the consent call, the bind-check
    # executor (profile uk-ob) and the stub extension.
    for src in par_clients:
        c = copy.deepcopy(src)
        c["clientId"] = src["clientId"].replace(PAR_SUFFIX, UKPAR_SUFFIX)
        c["attributes"]["acr.loa.map"] = ob_acr_map
        c["serviceAccountsEnabled"] = True
        c["redirectUris"] = [u for u in c["redirectUris"] if f"/test/a/{ALIAS['p2']}/" not in u]
        add_redirects(c, ALIAS["p3"])
        c["protocolMappers"].append(stub_mapper())
        uk["clients"].append(c)
        uk["roles"]["client"][c["clientId"]] = [{
            "name": "uk-ob-par-role",
            "description": "Client role for the consent-carrier PAR group",
            "composite": False,
            "clientRole": True,
        }]
    uk["clientPolicies"]["policies"].append({
        "name": "uk-ob-par-policy",
        "description": "FAPI 1.0 Advanced with PAR and the UK-OB intent bind check",
        "enabled": True,
        "conditions": [{"condition": "client-roles", "configuration": {"roles": ["uk-ob-par-role"]}}],
        "profiles": ["fapi-1-advanced", "fapi-1-advanced-par", "uk-ob"],
    })
    profiles = {p["name"] for p in uk["clientProfiles"]["profiles"]}
    if not {"fapi-1-advanced-par", "uk-ob"} <= profiles:
        die("expected client profiles missing: " + str(profiles))
    for p in uk["clientProfiles"]["profiles"]:
        for ex in p["executors"]:
            if ex["executor"] == "intent-client-bind-checker":
                ex["configuration"]["intent-client-bind-check-endpoint"] = \
                    f"http://{RS_ALIAS}:3000/check-intent-client-bound"
    uk.setdefault("attributes", {})["parRequestUriLifespan"] = adv["attributes"].get("parRequestUriLifespan", "60")
    dump(REALMS / "realm-prd040.json", uk)
    summary = {
        "p1": [c["clientId"] for c in uk_clients],
        "p2": [c["clientId"] for c in par_clients],
        "p3": [c["clientId"].replace(PAR_SUFFIX, UKPAR_SUFFIX) for c in par_clients],
        "policies": [(p["name"], p["profiles"]) for p in uk["clientPolicies"]["policies"]],
        "uk_ob_executors": [p["executors"] for p in uk["clientProfiles"]["profiles"] if p["name"] == "uk-ob"],
    }
    print(json.dumps(summary, indent=1))


def realias(cfg, alias, description):
    text = json.dumps(cfg).replace("/test/a/keycloak/", f"/test/a/{alias}/")
    out = json.loads(text)
    out["alias"] = alias
    out["description"] = description
    return out


def ob_resource(cfg):
    cfg["resource"] = {
        "resourceUrl": OB_BASE,
        "resourceUrlAccountRequests": OB_BASE,
        "resourceUrlAccountsResource": OB_BASE,
    }


def build_configs():
    out_dir = CFG / "prd040"
    pre_26_4 = version_tuple(KC_VERSION) < (26, 4, 0)
    for auth, uk_name, par_name in (
            ("private-key", "fapi-uk-ob-private-key-PS256-PS256-automated.json",
             "fapi1-advanced-par-private-key-PS256-PS256-automated.json"),
            ("mtls", "fapi-uk-ob-mtls-PS256-PS256-automated.json",
             "fapi1-advanced-par-mtls-PS256-PS256-automated.json")):
        p1 = realias(load(CFG / "fapi-uk-ob" / uk_name), ALIAS["p1"],
                     f"PRD-040 P1 UK-OB carrier by value, {auth}, Keycloak {KC_VERSION}")
        ob_resource(p1)
        dump(out_dir / f"p1-{auth}.json", p1)

        par = load(CFG / "fapi1-advanced" / par_name)
        text = json.dumps(par)
        if pre_26_4:
            # Before Keycloak 26.4 the PAR login page is served at the authorization
            # URL itself; the SIG changed this match when it moved to 26.4.0.
            text = text.replace("/realms/test/login-actions/authenticate*",
                                "/realms/test/protocol/openid-connect/auth*")
        par = json.loads(text)
        p2 = realias(par, ALIAS["p2"], f"PRD-040 P2 FAPI1 Advanced PAR, {auth}, Keycloak {KC_VERSION}")
        dump(out_dir / f"p2-{auth}.json", p2)

        p3 = json.loads(json.dumps(par).replace(PAR_SUFFIX, UKPAR_SUFFIX))
        p3 = realias(p3, ALIAS["p3"], f"PRD-040 P3 UK-OB carrier pushed, {auth}, Keycloak {KC_VERSION}")
        ob_resource(p3)
        dump(out_dir / f"p3-{auth}.json", p3)
    print("configs written to " + str(out_dir) + " (PAR login match at authorization URL: " + str(pre_26_4) + ")")


def patch_images():
    # Keycloak image: install the stub extension jar.
    dockerfile = KC_DIR / "Dockerfile"
    text = dockerfile.read_text()
    line = "COPY ${KEYCLOAK_SPI_0} /opt/keycloak/providers/"
    if line not in text:
        die("SPI copy line not found in the Keycloak Dockerfile")
    text = text.replace(line, "COPY prd040-consent-stub.jar /opt/keycloak/providers/")
    dockerfile.write_text(text)
    if not (KC_DIR / "prd040-consent-stub.jar").is_file():
        die("stub extension jar missing; run build-extension.sh first")

    # Discovery metadata override: keep the SIG's acr and claims lists, add the
    # UK-OB acr values, drop authorization_details_types_supported so the
    # discovery document shows only what Keycloak itself advertises for it.
    meta_path = KC_DIR / "oidf.json"
    meta = load(meta_path)
    meta.pop("authorization_details_types_supported", None)
    for acr in ("urn:openbanking:psd2:sca", "urn:openbanking:psd2:ca"):
        if acr not in meta["acr_values_supported"]:
            meta["acr_values_supported"].append(acr)
    dump(meta_path, meta)

    # Resource server: replace the SIG's Go stub with this lab's consent-aware stub.
    rs = KCSIG / "spec/entities/resource-server"
    for f in rs.iterdir():
        if f.is_file():
            f.unlink()
    for name in ("server.py", "Dockerfile"):
        shutil.copy(HERE / "resource-server" / name, rs / name)
    # Give it a host name without "_": java.net.URI (used by the extension's
    # HTTP client) does not accept an underscore in a host name.
    compose = KCSIG / "docker-compose.yml"
    text = compose.read_text()
    anchor = "      context: ./spec/entities/resource-server\n"
    if text.count(anchor) != 1:
        die("resource server block not found in docker-compose.yml")
    alias = "    networks:\n      default:\n        aliases:\n          - " + RS_ALIAS + "\n"
    compose.write_text(text.replace(anchor, anchor + alias))

    # Conformance suite build: the FAPI1 Advanced Final plan refuses PAR for the
    # openbanking_uk profile in its certification-profile naming. Remove only
    # "par ||" from that one condition so the suite's own modules can run the
    # consent carrier through PAR. The build log shows the line before and after.
    suite_df = KCSIG / "conformance-suite/conformance-suite/Dockerfile"
    text = suite_df.read_text()
    anchor = "RUN cd conformance-suite && mv -f run-tests.sh ./.gitlab-ci && mvn package -DskipTests"
    if anchor not in text:
        die("suite build anchor not found")
    plan = "src/main/java/net/openid/conformance/fapi1advancedfinal/FAPI1AdvancedFinalTestPlan.java"
    patch = (
        f"RUN cd conformance-suite && echo 'PRD040 guard before:' && grep -n -A2 'if (par || jarm) {{' {plan} "
        f"&& sed -i 's/if (par || jarm) {{/if (jarm) {{/' {plan} "
        f"&& echo 'PRD040 guard after:' && grep -n -A2 'case \"openbanking_uk\"' {plan} "
        f"&& ! grep -q 'if (par || jarm) {{' {plan}\n"
    )
    suite_df.write_text(text.replace(anchor, patch + anchor))


if __name__ == "__main__":
    build_realm()
    build_configs()
    patch_images()
    print("prepare: done")
