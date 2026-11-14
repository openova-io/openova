#!/usr/bin/env python3
"""Throwaway RS256 key, key set and test tokens for lab test AIP-030 (spike branch only).

  mint.py keygen DIR            -> DIR/key.pem, DIR/jwks.json (kid spike-1)
  mint.py token DIR CLAIMS_JSON -> prints a signed JWT (iat, nbf, exp, jti added)
  mint.py decode JWT            -> prints {"header":..., "claims":...} without verifying
"""
import base64
import json
import os
import subprocess
import sys
import time
import uuid


def b64u(b):
    return base64.urlsafe_b64encode(b).rstrip(b"=").decode()


def unb64u(s):
    return base64.urlsafe_b64decode(s + "=" * (-len(s) % 4))


def keygen(d):
    os.makedirs(d, exist_ok=True)
    key = os.path.join(d, "key.pem")
    subprocess.run(["openssl", "genrsa", "-out", key, "2048"], check=True, capture_output=True)
    mod = subprocess.run(["openssl", "rsa", "-in", key, "-noout", "-modulus"], check=True,
                         capture_output=True, text=True).stdout.strip().split("=", 1)[1]
    jwk = {"kty": "RSA", "kid": "spike-1", "use": "sig", "alg": "RS256",
           "n": b64u(bytes.fromhex(mod)), "e": b64u((65537).to_bytes(3, "big"))}
    with open(os.path.join(d, "jwks.json"), "w") as f:
        json.dump({"keys": [jwk]}, f)


def token(d, claims_json):
    claims = json.loads(claims_json)
    now = int(time.time())
    claims.setdefault("iat", now)
    claims.setdefault("nbf", now - 5)
    claims.setdefault("exp", now + 4 * 3600)
    claims.setdefault("jti", uuid.uuid4().hex)
    h = b64u(json.dumps({"alg": "RS256", "typ": "JWT", "kid": "spike-1"}, separators=(",", ":")).encode())
    p = b64u(json.dumps(claims, separators=(",", ":")).encode())
    sig = subprocess.run(["openssl", "dgst", "-sha256", "-sign", os.path.join(d, "key.pem")],
                         input=("%s.%s" % (h, p)).encode(), check=True, capture_output=True).stdout
    print("%s.%s.%s" % (h, p, b64u(sig)))


def decode(tok):
    tok = tok.strip()
    if tok.lower().startswith("bearer "):
        tok = tok[7:]
    parts = tok.split(".")
    out = {}
    try:
        out["header"] = json.loads(unb64u(parts[0]))
        out["claims"] = json.loads(unb64u(parts[1]))
    except Exception as e:
        out["error"] = "not a JWT: %r" % e
    print(json.dumps(out))


if __name__ == "__main__":
    cmd = sys.argv[1]
    if cmd == "keygen":
        keygen(sys.argv[2])
    elif cmd == "token":
        token(sys.argv[2], sys.argv[3])
    elif cmd == "decode":
        decode(sys.argv[2])
    else:
        sys.exit("unknown command " + cmd)
