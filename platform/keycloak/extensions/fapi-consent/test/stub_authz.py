#!/usr/bin/env python3
"""stub_authz.py - a stand-in for the internal authorisation API, for the integration test.

It serves the three calls the extension makes, over a fixed set of consents and one customer,
and checks the bearer token of every call the way the product does: algorithm PS256, a key of
the realm's key set, the realm as issuer, the audience fingate-authz, a lifetime of at most 60
seconds that has not ended. It records every call; GET /_calls returns the record as JSON.

Consents (client tpp unless noted):
  c-auth, c-sso, c-reject   awaiting, account access
  c-other                   awaiting, client someone-else       -> 403 on the first call
  c-ledger                  awaiting; the customer call fails    -> 502
  any other identifier      unknown                              -> 404
Customer: scheme test-bank, subject alice -> cust-0001 with acc-0001-01 and acc-0001-02.

Standard library only. Usage: stub_authz.py --port <port> --issuer <url> --jwks <url>
"""
import argparse
import base64
import hashlib
import json
import sys
import threading
import time
import urllib.parse
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PERMISSIONS = ["ReadAccountsBasic", "ReadBalances"]
CONSENTS = {
    "c-auth": "tpp",
    "c-sso": "tpp",
    "c-reject": "tpp",
    "c-other": "someone-else",
    "c-ledger": "tpp",
}
ACCOUNTS = [
    {"id": "acc-0001-01", "displayName": "Main account", "maskedNumber": "****0101", "currency": "GBP"},
    {"id": "acc-0001-02", "displayName": "Savings", "maskedNumber": "****0102", "currency": "GBP"},
]


def b64d(s):
    return base64.urlsafe_b64decode(s + "=" * (-len(s) % 4))


def mgf1(seed, length):
    out = b""
    counter = 0
    while len(out) < length:
        out += hashlib.sha256(seed + counter.to_bytes(4, "big")).digest()
        counter += 1
    return out[:length]


def pss_verify(n, e, message, signature):
    """RSASSA-PSS with SHA-256, MGF1 with SHA-256 and a 32-byte salt (RFC 8017 section 8.1.2)."""
    hlen = 32
    k = (n.bit_length() + 7) // 8
    if len(signature) != k:
        return False
    s = int.from_bytes(signature, "big")
    if s >= n:
        return False
    m = pow(s, e, n)
    embits = n.bit_length() - 1
    emlen = (embits + 7) // 8
    if m.bit_length() > embits:
        return False
    em = m.to_bytes(emlen, "big")
    if em[-1] != 0xBC or emlen < 2 * hlen + 2:
        return False
    masked, h = em[: emlen - hlen - 1], em[emlen - hlen - 1 : -1]
    unused = 8 * emlen - embits
    if unused and masked[0] >> (8 - unused):
        return False
    db = bytearray(a ^ b for a, b in zip(masked, mgf1(h, emlen - hlen - 1)))
    if unused:
        db[0] &= 0xFF >> unused
    pad = emlen - 2 * hlen - 2
    if any(db[:pad]) or db[pad] != 1:
        return False
    salt = bytes(db[pad + 1 :])
    return hashlib.sha256(b"\0" * 8 + hashlib.sha256(message).digest() + salt).digest() == h


class Stub:
    def __init__(self, issuer, jwks_url):
        self.issuer = issuer
        self.jwks_url = jwks_url
        self.keys = {}
        self.calls = []
        self.state = {cid: "awaiting" for cid in CONSENTS}
        self.lock = threading.Lock()

    def key(self, kid):
        if kid not in self.keys:
            with urllib.request.urlopen(self.jwks_url, timeout=10) as r:
                for k in json.load(r).get("keys", []):
                    if k.get("kty") == "RSA" and "kid" in k:
                        self.keys[k["kid"]] = (int.from_bytes(b64d(k["n"]), "big"), int.from_bytes(b64d(k["e"]), "big"))
        return self.keys.get(kid)

    def bearer(self, header):
        """Returns None when the token is accepted, else the reason it is refused."""
        if not header or not header.startswith("Bearer "):
            return "no bearer token"
        parts = header[len("Bearer ") :].split(".")
        if len(parts) != 3:
            return "not a signed token"
        head, body = json.loads(b64d(parts[0])), json.loads(b64d(parts[1]))
        if head.get("alg") != "PS256":
            return "algorithm %r" % head.get("alg")
        key = self.key(head.get("kid", ""))
        if key is None:
            return "unknown key %r" % head.get("kid")
        if not pss_verify(key[0], key[1], (parts[0] + "." + parts[1]).encode(), b64d(parts[2])):
            return "bad signature"
        if body.get("iss") != self.issuer:
            return "issuer %r" % body.get("iss")
        aud = body.get("aud")
        if "fingate-authz" not in (aud if isinstance(aud, list) else [aud]):
            return "audience %r" % aud
        now = int(time.time())
        if not isinstance(body.get("exp"), int) or body["exp"] <= now:
            return "expired"
        if not isinstance(body.get("iat"), int) or body["iat"] > now + 60 or body["exp"] - body["iat"] > 60:
            return "lifetime"
        if body.get("sub") != "fapi-consent":
            return "subject %r" % body.get("sub")
        return None


def handler(stub):
    class Handler(BaseHTTPRequestHandler):
        def log_message(self, fmt, *args):
            sys.stderr.write("stub: " + (fmt % args) + "\n")

        def answer(self, status, body):
            data = json.dumps(body).encode()
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

        def do_GET(self):
            url = urllib.parse.urlsplit(self.path)
            if url.path == "/_calls":
                with stub.lock:
                    return self.answer(200, stub.calls)
            self.handle_call("GET", url, None)

        def do_POST(self):
            url = urllib.parse.urlsplit(self.path)
            length = int(self.headers.get("Content-Length") or 0)
            try:
                body = json.loads(self.rfile.read(length) or b"null")
            except ValueError:
                body = "not json"
            self.handle_call("POST", url, body)

        def handle_call(self, method, url, body):
            refused = stub.bearer(self.headers.get("Authorization"))
            query = urllib.parse.parse_qs(url.query)
            with stub.lock:
                stub.calls.append({"method": method, "path": url.path, "query": query, "body": body, "bearer": refused or "ok"})
            if refused:
                return self.answer(401, {"error": "invalid_token", "reason": refused})
            parts = url.path.split("/")
            # ['', 'internal', 'v1', 'authorisations', '<id>'(, '<leaf>')]
            if len(parts) < 5 or parts[1:4] != ["internal", "v1", "authorisations"]:
                return self.answer(404, {"error": "no-route"})
            cid = urllib.parse.unquote(parts[4])
            leaf = parts[5] if len(parts) > 5 else None
            if cid not in CONSENTS:
                return self.answer(404, {"error": "consent-not-found"})
            if method == "GET" and leaf is None:
                if query.get("client_id") != [CONSENTS[cid]]:
                    return self.answer(403, {"error": "client-mismatch"})
                if stub.state[cid] != "awaiting":
                    return self.answer(409, {"error": "not-awaiting"})
                return self.answer(200, {"consentId": cid, "family": "account-access", "clientId": CONSENTS[cid],
                                         "state": "awaiting", "expiresAt": "2026-12-31T00:00:00Z",
                                         "summary": {"permissions": PERMISSIONS}})
            if method == "POST" and leaf == "customer":
                if cid == "c-ledger":
                    return self.answer(502, {"error": "ledger-unavailable"})
                ident = (body or {}).get("identity", {})
                if ident.get("scheme") != "test-bank" or ident.get("subject") != "alice":
                    return self.answer(404, {"error": "customer-unknown"})
                return self.answer(200, {"customerId": "cust-0001", "accounts": ACCOUNTS})
            if method == "POST" and leaf == "decision":
                if stub.state[cid] != "awaiting":
                    return self.answer(409, {"error": "not-awaiting"})
                owned = {a["id"] for a in ACCOUNTS}
                if body.get("decision") == "authorise":
                    if not set(body.get("accounts", [])) <= owned:
                        return self.answer(422, {"error": "account-not-owned"})
                    stub.state[cid] = "authorised"
                else:
                    stub.state[cid] = "rejected"
                return self.answer(200, {"state": stub.state[cid]})
            return self.answer(404, {"error": "no-route"})

    return Handler


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--port", type=int, required=True)
    p.add_argument("--issuer", required=True)
    p.add_argument("--jwks", required=True)
    a = p.parse_args()
    server = ThreadingHTTPServer(("127.0.0.1", a.port), handler(Stub(a.issuer, a.jwks)))
    sys.stderr.write("stub: serving on 127.0.0.1:%d\n" % a.port)
    server.serve_forever()


if __name__ == "__main__":
    main()
