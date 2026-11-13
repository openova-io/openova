#!/usr/bin/env python3
"""Stub resource server for lab test PRD-040 (never shipped).

It sits behind the conformance environment's API gateway, which has already
checked the client certificate, introspected the access token and matched the
certificate binding. This stub only plays the consent side of an open-banking
resource server, Read/Write API 4.0 paths:

  POST /open-banking/v4.0/aisp/account-access-consents   create a consent (AWAU), bound to the token's azp
  POST /check-intent-client-bound                        Keycloak's built-in bind-check executor calls this
  POST /internal/consent-step                            the stub Keycloak extension marks the consent AUTH
  GET  /open-banking/v4.0/aisp/accounts                  served only if the ACCESS token carries the consent
                                                         claim of an AUTH consent of the same client
  GET  anything else                                     plain FAPI resource, no consent rule

Every decision is printed as one JSON line, so the job can count them.
"""
import base64
import json
import secrets
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

CLAIM = "openbanking_intent_id"
CONSENTS = {}
LOCK = threading.Lock()


def log(event, **fields):
    print(json.dumps({"event": event, **fields}, sort_keys=True), flush=True)


def token_claims(header):
    """Payload of the bearer JWT. The gateway has verified it already."""
    if not header or " " not in header:
        return {}
    token = header.split(" ", 1)[1].strip()
    parts = token.split(".")
    if len(parts) < 2:
        return {}
    payload = parts[1] + "=" * (-len(parts[1]) % 4)
    try:
        return json.loads(base64.urlsafe_b64decode(payload))
    except ValueError:
        return {}


def now():
    return time.strftime("%Y-%m-%dT%H:%M:%S+00:00", time.gmtime())


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):  # quiet default access log
        return

    def send_json(self, code, body):
        data = json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json; charset=UTF-8")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def read_json(self):
        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(length) if length else b""
        try:
            return json.loads(raw or b"{}")
        except ValueError:
            return {}

    def do_POST(self):
        path = self.path.split("?", 1)[0]
        body = self.read_json()
        if path.endswith("/account-access-consents"):
            client = token_claims(self.headers.get("Authorization")).get("azp")
            if not client:
                log("consent_create_refused", reason="no azp in token")
                return self.send_json(401, {"Code": "401", "Message": "no client in token"})
            consent_id = "aac-" + secrets.token_hex(8)
            with LOCK:
                CONSENTS[consent_id] = {"client": client, "status": "AWAU", "subject": None}
            log("consent_created", consent=consent_id, client=client)
            data = dict(body.get("Data") or {})
            data.update({
                "ConsentId": consent_id,
                "Status": "AWAU",
                "CreationDateTime": now(),
                "StatusUpdateDateTime": now(),
            })
            self_url = "https://rs.keycloak-fapi.org/open-banking/v4.0/aisp/account-access-consents/" + consent_id
            return self.send_json(201, {"Data": data, "Risk": {}, "Links": {"Self": self_url}, "Meta": {}})
        if path == "/check-intent-client-bound":
            with LOCK:
                consent = CONSENTS.get(body.get("intent_id"))
            bound = bool(consent and consent["client"] == body.get("client_id"))
            log("bind_check", consent=body.get("intent_id"), client=body.get("client_id"), bound=bound)
            return self.send_json(200, {"is_bound": bound})
        if path == "/internal/consent-step":
            with LOCK:
                consent = CONSENTS.get(body.get("consent_id"))
                ok = bool(consent and consent["client"] == body.get("client_id"))
                if ok:
                    consent["status"] = "AUTH"
                    consent["subject"] = body.get("subject")
            log("consent_step", consent=body.get("consent_id"), client=body.get("client_id"), ok=ok)
            if not ok:
                return self.send_json(404, {"Code": "404", "Message": "unknown consent for this client"})
            return self.send_json(200, {"status": "AUTH"})
        log("unknown_post", path=path)
        return self.send_json(404, {"Code": "404", "Message": "not found"})

    def do_GET(self):
        path = self.path.split("?", 1)[0]
        claims = token_claims(self.headers.get("Authorization"))
        client = claims.get("azp")
        if path.startswith("/open-banking/"):
            consent_id = claims.get(CLAIM)
            with LOCK:
                consent = CONSENTS.get(consent_id) if consent_id else None
            reason = None
            if not consent_id:
                reason = "no consent claim in access token"
            elif consent is None:
                reason = "unknown consent"
            elif consent["client"] != client:
                reason = "consent belongs to another client"
            elif consent["status"] != "AUTH":
                reason = "consent not authorised"
            if reason:
                log("accounts_refused", consent=consent_id, client=client, reason=reason)
                return self.send_json(403, {"Code": "403", "Message": reason})
            log("accounts_served", consent=consent_id, client=client)
            account = {"AccountId": "100004000000000000000002", "Currency": "OMR",
                       "AccountType": "Personal", "AccountSubType": "CurrentAccount"}
            return self.send_json(200, {"Data": {"Account": [account]},
                                        "Links": {"Self": "https://rs.keycloak-fapi.org" + path},
                                        "Meta": {"TotalPages": 1}})
        log("plain_resource_served", client=client, path=path)
        return self.send_json(200, {"accounts": [{"name": "John"}, {"name": "Sary"}]})


if __name__ == "__main__":
    log("start", port=3000)
    ThreadingHTTPServer(("0.0.0.0", 3000), Handler).serve_forever()
