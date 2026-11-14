#!/usr/bin/env python3
"""drive.py - the integration test of the fapi-consent extension.

It plays a third party and a customer against a Keycloak that runs the extension with the realm
of test/realm.json and the stand-in API of test/stub_authz.py, and asserts what the extension
must do. Each case prints one line, "ok <case>" or "FAIL <case>: <why>"; the exit code is 1 when
a case fails, 0 when every case passed.

The client secret and the user's password are made up by integration.sh for each run and come in
the environment variables FAPI_TEST_CLIENT_SECRET and FAPI_TEST_USER_PASSWORD.

Standard library only. Usage: drive.py --kc <keycloak base address> --stub <stub base address>
"""
import argparse
import base64
import html
import http.cookiejar
import json
import os
import re
import sys
import urllib.error
import urllib.parse
import urllib.request

REALM = "fapi"
CLIENT, SECRET = "tpp", os.environ["FAPI_TEST_CLIENT_SECRET"]
REDIRECT = "http://127.0.0.1:18099/cb"
USER, PASSWORD = "alice", os.environ["FAPI_TEST_USER_PASSWORD"]
CLAIM = "openbanking_intent_id"
FAILED = []


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class LoopbackPolicy(http.cookiejar.DefaultCookiePolicy):
    """Sends a Secure cookie over plain HTTP, as a browser does for the loopback address."""

    def return_ok_secure(self, cookie, request):
        return True


class Browser:
    """A cookie jar and a client that does not follow redirects."""

    def __init__(self):
        jar = http.cookiejar.CookieJar(policy=LoopbackPolicy())
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar), NoRedirect())

    def request(self, url, form=None):
        data = urllib.parse.urlencode(form, doseq=True).encode() if form is not None else None
        try:
            with self.opener.open(urllib.request.Request(url, data=data), timeout=30) as r:
                return r.status, dict(r.headers), r.read().decode()
        except urllib.error.HTTPError as e:
            return e.code, dict(e.headers), e.read().decode()


def check(name, condition, why):
    if condition:
        print("ok   " + name)
    else:
        print("FAIL %s: %s" % (name, why))
        FAILED.append(name)


def claims(consent, essential=True):
    return json.dumps({"id_token": {CLAIM: {"value": consent, "essential": essential}}})


def payload(jwt):
    part = jwt.split(".")[1]
    return json.loads(base64.urlsafe_b64decode(part + "=" * (-len(part) % 4)))


def form_action(page, form_id):
    m = re.search(r'<form[^>]*id="%s"[^>]*action="([^"]+)"' % form_id, page) or \
        re.search(r'<form[^>]*action="([^"]+)"[^>]*id="%s"' % form_id, page)
    return html.unescape(m.group(1)) if m else None


class Run:
    def __init__(self, kc, stub):
        self.kc = kc.rstrip("/")
        self.stub = stub.rstrip("/")
        self.oidc = "%s/realms/%s/protocol/openid-connect" % (self.kc, REALM)

    def calls(self):
        with urllib.request.urlopen(self.stub + "/_calls", timeout=10) as r:
            return json.load(r)

    def par(self, browser, extra, client=CLIENT, auth=None):
        form = {"client_id": client, "response_type": "code", "scope": "openid", "redirect_uri": REDIRECT,
                "state": "s1", "nonce": "n1"}
        form.update(auth if auth is not None else {"client_secret": SECRET})
        form.update(extra)
        return browser.request(self.oidc + "/ext/par/request", form)

    def authorise_start(self, browser, extra):
        """Pushes a request and opens the authorisation address. Returns (status, headers, page)."""
        status, _, body = self.par(browser, extra)
        if status != 201:
            raise AssertionError("PAR answered %s: %s" % (status, body[:300]))
        uri = json.loads(body)["request_uri"]
        return browser.request(self.oidc + "/auth?" + urllib.parse.urlencode({"client_id": CLIENT, "request_uri": uri}))

    def sign_in(self, browser, page):
        action = form_action(page, "kc-form-login")
        if action is None:
            raise AssertionError("no login form: " + page[:300])
        return browser.request(action, {"username": USER, "password": PASSWORD})

    def tokens(self, browser, location):
        q = urllib.parse.parse_qs(urllib.parse.urlsplit(location).query)
        if "code" not in q:
            raise AssertionError("no code in " + location)
        status, _, body = browser.request(self.oidc + "/token", {
            "grant_type": "authorization_code", "code": q["code"][0], "redirect_uri": REDIRECT,
            "client_id": CLIENT, "client_secret": SECRET})
        if status != 200:
            raise AssertionError("token answered %s: %s" % (status, body[:300]))
        return json.loads(body)

    def decide(self, browser, page, decision, accounts):
        action = form_action(page, "fapi-consent-form")
        if action is None:
            raise AssertionError("no consent form: " + page[:300])
        return browser.request(action, {"decision": decision, "account": accounts})


def error_of(headers):
    q = urllib.parse.parse_qs(urllib.parse.urlsplit(headers.get("Location", "")).query)
    return q.get("error", [None])[0], q.get("error_description", [None])[0]


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--kc", required=True)
    p.add_argument("--stub", required=True)
    a = p.parse_args()
    run = Run(a.kc, a.stub)

    # 1. The PAR endpoint answers a failed client authentication with 401 invalid_client.
    b = Browser()
    status, _, body = run.par(b, {}, client="tpp-jwt", auth={
        "client_assertion_type": "urn:ietf:params:oauth:client-assertion-type:jwt-bearer", "client_assertion": "a.b.c"})
    err = json.loads(body).get("error") if body.startswith("{") else body[:100]
    check("PAR: a bad client assertion gets 401 invalid_client", (status, err) == (401, "invalid_client"), (status, err))
    status, _, body = run.par(b, {}, auth={"client_secret": "wrong"})
    err = json.loads(body).get("error") if body.startswith("{") else body[:100]
    check("PAR: a wrong client secret gets 401 invalid_client", (status, err) == (401, "invalid_client"), (status, err))
    status, _, body = run.par(b, {})
    check("PAR: a good request still gets 201 and a request_uri",
          status == 201 and json.loads(body).get("request_uri", "").startswith("urn:ietf:params:oauth:request_uri:"),
          (status, body[:200]))

    # 2. Sign-in, consent page, authorise one account: both tokens carry the consent.
    b = Browser()
    _, _, page = run.authorise_start(b, {"claims": claims("c-auth")})
    status, headers, page = run.sign_in(b, page)
    check("consent: the page follows the sign-in", status == 200 and 'id="fapi-consent-form"' in page, (status, page[:300]))
    check("consent: the page lists the permissions and the accounts",
          all(s in page for s in ("ReadAccountsBasic", "ReadBalances", "Main account", "****0102", "acc-0001-01")),
          page[:500])
    check("consent: the client name is escaped", "&lt;b&gt;Lettings" in page and "<b>Lettings" not in page, "raw markup on the page")
    status, headers, page = run.decide(b, page, "authorise", [])
    check("consent: authorise with no account shows the page again with an error",
          status == 200 and "Choose at least one account." in page and 'id="fapi-consent-form"' in page, (status, page[:300]))
    status, headers, _ = run.decide(b, page, "authorise", ["acc-0001-01"])
    check("consent: authorise redirects with a code", status == 302 and "code=" in headers.get("Location", ""), (status, headers.get("Location")))
    t = run.tokens(b, headers["Location"])
    at, it = payload(t["access_token"]), payload(t["id_token"])
    check("tokens: the access token carries the consent", at.get(CLAIM) == "c-auth", at.get(CLAIM))
    check("tokens: the ID token carries the consent", it.get(CLAIM) == "c-auth", it.get(CLAIM))
    status, _, body = b.request(run.oidc + "/token", {"grant_type": "refresh_token", "refresh_token": t["refresh_token"],
                                                     "client_id": CLIENT, "client_secret": SECRET})
    check("tokens: a refreshed access token carries the consent",
          status == 200 and payload(json.loads(body)["access_token"]).get(CLAIM) == "c-auth", (status, body[:200]))
    issuer = at.get("iss")

    calls = [c for c in run.calls() if "/c-auth" in c["path"]]
    shape = [(c["method"], c["path"].rsplit("/", 1)[-1]) for c in calls]
    check("api: three calls in order", shape == [("GET", "c-auth"), ("POST", "customer"), ("POST", "decision")], shape)
    if len(calls) == 3:
        check("api: the first call names the client", calls[0]["query"].get("client_id") == [CLIENT], calls[0]["query"])
        check("api: the customer call sends the identity",
              calls[1]["body"] == {"identity": {"scheme": "test-bank", "subject": USER, "attributes": {}}}, calls[1]["body"])
        d = calls[2]["body"]
        check("api: the decision sends customer, decision and the chosen account",
              {k: d.get(k) for k in ("customerId", "decision", "accounts", "acr")} ==
              {"customerId": "cust-0001", "decision": "authorise", "accounts": ["acc-0001-01"], "acr": "1"}, d)
        check("api: authTime is an RFC 3339 time in UTC",
              re.fullmatch(r"\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ", d.get("authTime", "")) is not None, d.get("authTime"))
    check("api: every bearer token was accepted", all(c["bearer"] == "ok" for c in run.calls()),
          [c["bearer"] for c in run.calls()])

    # 3. The same browser, a request without a consent: no page, and no stale consent in the tokens.
    before = len(run.calls())
    status, headers, _ = run.authorise_start(b, {})
    check("no consent: the existing session goes straight to a code", status == 302 and "code=" in headers.get("Location", ""),
          (status, headers.get("Location")))
    if status == 302:
        t = run.tokens(b, headers["Location"])
        check("no consent: the access token carries no consent", CLAIM not in payload(t["access_token"]), payload(t["access_token"]).get(CLAIM))
        check("no consent: the ID token carries no consent", CLAIM not in payload(t["id_token"]), payload(t["id_token"]).get(CLAIM))
    check("no consent: the API was not called", len(run.calls()) == before, run.calls()[before:])

    # 4. The same browser, a consent: the page comes without a sign-in, and acr is 0.
    status, _, page = run.authorise_start(b, {"claims": claims("c-sso")})
    check("reused session: the consent page comes without a sign-in", status == 200 and 'id="fapi-consent-form"' in page, (status, page[:300]))
    status, headers, _ = run.decide(b, page, "authorise", ["acc-0001-02"])
    if status == 302 and "code=" in headers.get("Location", ""):
        t = run.tokens(b, headers["Location"])
        check("reused session: the token carries the new consent", payload(t["access_token"]).get(CLAIM) == "c-sso", payload(t["access_token"]).get(CLAIM))
    else:
        check("reused session: authorise redirects with a code", False, (status, headers.get("Location")))
    d = [c for c in run.calls() if c["path"].endswith("/c-sso/decision")]
    check("reused session: the decision sends acr 0", len(d) == 1 and d[0]["body"].get("acr") == "0", d)

    # 5. A rejection, and each refusal of the API, ends with access_denied and the reason.
    for consent, decision, want in (("c-reject", "reject", "rejected"), ("c-missing", None, "consent-not-found"),
                                    ("c-other", None, "client-mismatch"), ("c-ledger", None, "ledger-unavailable")):
        b = Browser()
        _, _, page = run.authorise_start(b, {"claims": claims(consent)})
        status, headers, page = run.sign_in(b, page)
        if decision is not None:
            status, headers, page = run.decide(b, page, decision, [])
        check("refusal %s: the client gets access_denied with %s" % (consent, want),
              status == 302 and error_of(headers) == ("access_denied", want), (status, headers.get("Location")))
    d = [c for c in run.calls() if c["path"].endswith("/c-reject/decision")]
    check("refusal: the rejection is recorded", len(d) == 1 and d[0]["body"].get("decision") == "reject", d)
    check("refusal: an unknown consent stops after the first call",
          [c["path"] for c in run.calls() if "/c-missing" in c["path"]] == ["/internal/v1/authorisations/c-missing"],
          [c["path"] for c in run.calls() if "/c-missing" in c["path"]])

    # 6. A claim that is not essential is no consent.
    b = Browser()
    before = len(run.calls())
    _, _, page = run.authorise_start(b, {"claims": claims("c-auth", essential=False)})
    status, headers, _ = run.sign_in(b, page)
    if status == 302 and "code=" in headers.get("Location", ""):
        t = run.tokens(b, headers["Location"])
        check("not essential: the token carries no consent", CLAIM not in payload(t["id_token"]), payload(t["id_token"]).get(CLAIM))
    else:
        check("not essential: the sign-in redirects with a code", False, (status, headers.get("Location")))
    check("not essential: the API was not called", len(run.calls()) == before, run.calls()[before:])

    check("issuer: every bearer token named the realm as its issuer", issuer == "%s/realms/%s" % (run.kc, REALM), issuer)
    print("drive.py: %d failed" % len(FAILED))
    return 1 if FAILED else 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except AssertionError as e:
        print("FAIL the run stopped: %s" % e)
        sys.exit(1)
