"""Analyse what the stub received in spike PRD-070.

For every message the browser walk sent, find the request the stub captured
and classify its Authorization header (and the extra X-Spike-* headers):
  absent / empty / literal-placeholder / static-endpoint-key /
  user-access-token (a JWT signed by the test realm, typ Bearer, sub of the
  signed-in user) / other-jwt / other.
Usage: python analyse.py <label> <image> <capture.jsonl> <walk.json> <issuer> <ca.pem> <out-dir>
"""
import json
import ssl
import sys
import urllib.parse
import urllib.request

import jwt

label, image, capture_path, walk_path, issuer, ca_path, out_dir = sys.argv[1:8]
STATIC_KEY = "static-endpoint-key-not-the-user"
ctx = ssl.create_default_context(cafile=ca_path)
kc_base = issuer.split("/realms/")[0]


def get_json(url, token=None, data=None):
    req = urllib.request.Request(url, data=data)
    if token:
        req.add_header("Authorization", f"Bearer {token}")
    with urllib.request.urlopen(req, context=ctx, timeout=30) as resp:
        return json.load(resp)


discovery = get_json(f"{issuer}/.well-known/openid-configuration")
jwks = get_json(discovery["jwks_uri"])
sig_keys = {k["kid"]: k for k in jwks["keys"] if k.get("use", "sig") == "sig"}
admin = get_json(
    f"{kc_base}/realms/master/protocol/openid-connect/token",
    data=urllib.parse.urlencode(
        {"grant_type": "password", "client_id": "admin-cli", "username": "admin", "password": "admin"}
    ).encode(),
)["access_token"]
user_ids = {}
for name in ("alice", "bob"):
    found = get_json(f"{kc_base}/admin/realms/spike/users?username={name}&exact=true", token=admin)
    user_ids[name] = found[0]["id"] if found else None


def classify(value, user, received_at):
    if value is None:
        return {"kind": "absent"}
    raw = value[7:] if value.lower().startswith("bearer ") else value
    if raw == "":
        return {"kind": "empty", "value": value}
    if "{{" in raw:
        return {"kind": "literal-placeholder", "value": value}
    if raw == STATIC_KEY:
        return {"kind": "static-endpoint-key", "value": value}
    if raw.count(".") != 2:
        return {"kind": "other", "value": value[:80]}
    out = {"length": len(raw)}
    try:
        header = jwt.get_unverified_header(raw)
        key = jwt.PyJWK(sig_keys[header["kid"]])
        claims = jwt.decode(
            raw, key.key, algorithms=[header["alg"]],
            options={"verify_aud": False, "verify_exp": False, "verify_iss": False},
        )
        out["signature_valid"] = True
    except Exception as err:  # noqa: BLE001 - every failure is a measured value
        out["signature_valid"] = False
        out["signature_error"] = str(err)[:200]
        claims = jwt.decode(raw, options={"verify_signature": False})
    aud = claims.get("aud")
    aud_list = aud if isinstance(aud, list) else ([aud] if aud else [])
    out["claims"] = {k: claims.get(k) for k in ("iss", "typ", "azp", "aud", "sub", "preferred_username", "email", "iat", "exp")}
    out["iss_is_test_realm"] = claims.get("iss") == issuer
    out["sub_is_signed_in_user"] = claims.get("sub") == user_ids.get(user)
    out["aud_contains_cortex"] = "cortex" in aud_list
    out["unexpired_at_receipt"] = bool(claims.get("exp")) and claims["exp"] > received_at
    out["seconds_left_at_receipt"] = round(claims["exp"] - received_at, 1) if claims.get("exp") else None
    is_access = claims.get("typ") == "Bearer"
    good = out["signature_valid"] and out["iss_is_test_realm"] and out["sub_is_signed_in_user"]
    if good and is_access:
        out["kind"] = "user-access-token"
    elif good and claims.get("typ") == "ID":
        out["kind"] = "user-id-token"
    else:
        out["kind"] = "other-jwt"
    out["_raw"] = raw
    return out


walk = json.load(open(walk_path))
captures = [json.loads(line) for line in open(capture_path) if line.strip()] if capture_path else []
steps = []
for step in walk["steps"]:
    marker, user = step["marker"], step["user"]
    hit = next((c for c in captures if c["method"] == "POST" and marker in c["body"]), None)
    entry = {"marker": marker, "user": user, "request_reached_stub": hit is not None,
             "reply_shown": step.get("reply_shown"), "walk_error": step.get("error")}
    if hit:
        headers = {k.lower(): v for k, v in hit["headers"].items()}
        entry["path"] = hit["path"]
        entry["authorization"] = classify(headers.get("authorization"), user, hit["received_at"])
        entry["extra_headers"] = {}
        for name in ("x-spike-openid-token", "x-spike-openid-id-token", "x-spike-user-email", "x-spike-user-openidid"):
            val = headers.get(name)
            if val is not None and val.count(".") == 2 and len(val) > 60:
                c = classify(val, user, hit["received_at"])
                entry["extra_headers"][name] = c["kind"]
            else:
                entry["extra_headers"][name] = val if val is not None else "(absent)"
    else:
        entry["page_text_tail"] = step.get("page_text_tail")
    steps.append(entry)

by_marker = {s["marker"]: s for s in steps}


def auth_kind(marker):
    s = by_marker.get(marker)
    if not s:
        return "message-not-sent"
    if not s["request_reached_stub"]:
        return "no-request"
    return s["authorization"]["kind"]


first = [by_marker.get("prd070-alice-1"), by_marker.get("prd070-bob-1")]
forwards = all(s and s["request_reached_stub"] and s["authorization"]["kind"] == "user-access-token" for s in first)
distinct = forwards and first[0]["authorization"]["claims"]["sub"] != first[1]["authorization"]["claims"]["sub"]
second = by_marker.get("prd070-alice-2")
after_expiry = None
if second and second["request_reached_stub"]:
    a2 = second["authorization"]
    after_expiry = {
        "kind": a2["kind"],
        "unexpired_at_receipt": a2.get("unexpired_at_receipt"),
        "seconds_left_at_receipt": a2.get("seconds_left_at_receipt"),
        "same_token_as_first": bool(first[0] and first[0]["request_reached_stub"]
                                    and a2.get("_raw") == first[0]["authorization"].get("_raw")),
    }
elif second:
    after_expiry = {"kind": "no-request", "page_text_tail": second.get("page_text_tail")}

result = {
    "label": label,
    "image": image,
    "issuer": issuer,
    "walk_users": walk["users"],
    "steps": steps,
    "verdict": {
        "forwards_user_access_token": forwards,
        "two_users_two_subjects": distinct,
        "alice_1": auth_kind("prd070-alice-1"),
        "bob_1": auth_kind("prd070-bob-1"),
        "alice_2_after_expiry": after_expiry,
    },
}


def strip_raw(obj):
    if isinstance(obj, dict):
        return {k: strip_raw(v) for k, v in obj.items() if k != "_raw"}
    if isinstance(obj, list):
        return [strip_raw(v) for v in obj]
    return obj


public = strip_raw(result)
with open(f"{out_dir}/result.json", "w") as fh:
    json.dump(public, fh, indent=2)

lines = [f"### {label}: {image}", ""]
for u in walk["users"]:
    lines.append(f"- sign-in {u['user']}: {'ok' if u.get('signed_in') else 'FAILED ' + str(u.get('sign_in_error'))}"
                 f"; http errors seen by the browser: {u['http_errors'][:8]}")
for s in steps:
    if s["request_reached_stub"]:
        a = s["authorization"]
        detail = a["kind"]
        if "claims" in a:
            c = a["claims"]
            detail += (f" (signature_valid={a['signature_valid']}, typ={c['typ']}, azp={c['azp']}, aud={c['aud']},"
                       f" sub_is_signed_in_user={a['sub_is_signed_in_user']}, aud_contains_cortex={a['aud_contains_cortex']},"
                       f" seconds_left_at_receipt={a['seconds_left_at_receipt']})")
        elif "value" in a:
            detail += f" (value: {a['value']})"
        lines.append(f"- {s['marker']}: request at stub {s['path']}; Authorization = {detail};"
                     f" other headers = {s['extra_headers']}; reply shown = {s['reply_shown']}")
    else:
        lines.append(f"- {s['marker']}: NO request reached the stub; walk error = {s['walk_error']};"
                     f" page tail = {(s.get('page_text_tail') or '')[-200:]!r}")
lines.append(f"- VERDICT forwards_user_access_token={forwards} two_users_two_subjects={distinct}"
             f" alice_2_after_expiry={json.dumps(public['verdict']['alice_2_after_expiry'])}")
text = "\n".join(lines) + "\n"
open(f"{out_dir}/summary.md", "w").write(text)
print(text)
