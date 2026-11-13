"""Print Gitea's webhook rows and webhook deliveries from a copy of its sqlite file.

Secrets are never printed: the webhook secret and any authorization header
columns are skipped.
"""
import json
import sqlite3
import sys

db = sqlite3.connect(sys.argv[1])
db.row_factory = sqlite3.Row

cols = [r[1] for r in db.execute("PRAGMA table_info(webhook)")]
print("webhook columns:", ",".join(cols))
skip = {"secret", "header_authorization_encrypted", "meta"}
keep = [c for c in cols if c not in skip]
for r in db.execute("SELECT %s FROM webhook ORDER BY id" % ",".join(keep)):
    print("WEBHOOK", json.dumps(dict(r), sort_keys=True))

for r in db.execute(
    "SELECT id, hook_id, event_type, is_delivered, is_succeed, delivered, "
    "request_content, response_content FROM hook_task ORDER BY id"
):
    d = dict(r)
    req = json.loads(d.pop("request_content") or "{}")
    resp = json.loads(d.pop("response_content") or "{}")
    headers = dict(req.get("headers") or {})
    for k in list(headers):
        if k.lower() == "authorization":
            headers[k] = "<redacted>"
    d["request_url"] = req.get("url")
    d["request_method"] = req.get("http_method")
    d["request_headers"] = headers
    d["response_status"] = resp.get("status")
    d["response_body"] = (resp.get("body") or "")[:400]
    print("HOOKTASK", json.dumps(d, sort_keys=True))
