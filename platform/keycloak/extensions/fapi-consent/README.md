# fapi-consent — the open-banking consent step for Keycloak

A Keycloak extension for the open-banking product (`products/fingate`). It brings the consent
identifier from a third party's authorisation request to the customer, records the customer's
decision through the product's internal authorisation API, and writes the authorised consent
into the tokens. It holds no business rule: the API decides, the extension carries.

It is platform plumbing, not an Application Blueprint. The Keycloak chart
(`platform/keycloak/chart`) does not load it yet; that is a later change of the chart. Once
loaded, a realm uses it only when its browser flow and its clients name its providers, so every
other realm is unchanged.

## What it adds

| Provider | SPI | Identifier | What it does |
|---|---|---|---|
| `FapiConsentAuthenticator` | authenticator | `fapi-consent` | The consent step of the browser flow, after the customer is identified. |
| `FapiConsentClaimMapper` | protocol mapper | `fapi-consent-claim` | Writes the authorised consent into the access token and the ID token. |
| `FapiParRootEndpoint` | OIDC extension | `par` (order 100) | Keycloak's pushed-authorisation-request endpoint, corrected in one answer. |

### The consent carrier

The carrier is `intent-claim` (the result record `docs/ledger/plan/spikes/PRD-040.md`, outcome A).
The third party asks, inside the request object it pushes, for the essential ID-token claim
`openbanking_intent_id` whose value is the consent identifier:

```json
{"id_token": {"openbanking_intent_id": {"value": "<consent id>", "essential": true}}}
```

A request whose claim is missing, not essential, not a string, empty, longer than 128 characters
or holds a character outside printable ASCII carries no consent: the step lets it pass and its
tokens carry no consent claim.

### The consent step

For a request that carries a consent, after the customer is identified (a sign-in or an existing
session), the step makes the three calls of the internal authorisation API of `fingate-api`:

1. `GET /internal/v1/authorisations/{consentId}?client_id=<client>` reads the consent.
2. `POST /internal/v1/authorisations/{consentId}/customer` with
   `{"identity": {"scheme": <identity-scheme>, "subject": <user name>, "attributes": {}}}`
   resolves the customer and lists the customer's accounts.
3. The step shows the consent page (`theme-resources/templates/fapi-consent.ftl`): the
   permissions or the payment, and the accounts to choose. On the customer's answer it posts
   `POST /internal/v1/authorisations/{consentId}/decision` with
   `{"customerId", "decision": "authorise" or "reject", "accounts", "acr", "authTime"}`.

`acr` is `1` when the customer signed in during this request and `0` when an existing session
was reused, which is what Keycloak writes into the tokens of a realm without a
level-of-authentication map. `authTime` is the time of the sign-in as RFC 3339 in UTC.

When the API answers `{"state": "authorised"}`, the step stores the consent identifier as the
client note `fapi-consent.consent-id`, and the mapper writes it into the tokens as the claim
`openbanking_intent_id`, also into refreshed tokens. Every login clears that note first, so a
later request of the same client without a consent never carries an earlier consent.

Every other end of the step sends the OAuth error `access_denied` to the client's redirect
address, with the reason as `error_description`:

| Reason | When |
|---|---|
| `rejected` | the customer chose Deny |
| `consent-not-found` | the first call answered 404 |
| `client-mismatch` | the first call answered 403 |
| `not-awaiting` | the first or the decision call answered 409 |
| `customer-unknown` | the customer call answered 404 |
| `ledger-unavailable` | the customer call answered 502 |
| `account-not-owned` | the decision call answered 422 |
| `authorisation-unavailable` | any other answer, no answer, or the options below are not set |

Each call carries a bearer token that the extension signs with the realm's active PS256 key:
issuer the realm, audience `fingate-authz`, subject and authorised party `fapi-consent`, valid
for 60 seconds. The realm therefore needs an active RSA key for PS256.

### The PAR correction

Keycloak's pushed-authorisation-request endpoint answers a failed client authentication with
HTTP 401 and `invalid_request`; RFC 6749 section 5.2 requires `invalid_client`. Keycloak 26.3.3,
26.8.2 and its main branch have the same code, so no upgrade removes the need. `FapiParEndpoint`
changes that one answer and nothing else.

## Options

Two options of the authenticator's SPI scope, set when Keycloak starts:

| Option | Command-line form | Meaning |
|---|---|---|
| `authz-url` | `--spi-authenticator-fapi-consent-authz-url=<url>` | Base address of the internal authorisation API, for example the `:8081` listener of `fingate-api`. |
| `identity-scheme` | `--spi-authenticator-fapi-consent-identity-scheme=<scheme>` | The scheme under which the API resolves the user name to a customer; the ledger adapter defines the schemes it knows. |

Without both, Keycloak starts and every realm works; a login that carries a consent ends with
`authorisation-unavailable`.

## What the realm needs

These are realm settings, not part of this extension: the step `fapi-consent` (REQUIRED) after
the flow that identifies the customer, in the browser flow; the mapper `fapi-consent-claim` on
each third party's client, with "add to access token" and "add to ID token"; no built-in
claims-parameter mapper on those clients; an active PS256 key. `test/realm.json` is a working
example; `test/integration.sh` fills in its client secret and user password, made up for each run.

## Build, test, image

`build.sh` runs inside the upstream Keycloak image of the version the chart pins, compiles
against that image's jars, runs the unit tests (`src/test`) and writes the provider archive to
standard output:

```sh
docker run --rm --network none -v "$PWD/platform/keycloak/extensions/fapi-consent:/src:ro" \
  --entrypoint /bin/bash quay.io/keycloak/keycloak:26.3.3 /src/build.sh > fapi-consent.jar
bash platform/keycloak/extensions/fapi-consent/test/integration.sh quay.io/keycloak/keycloak:26.3.3 fapi-consent.jar
```

`test/integration.sh` starts Keycloak with the archive and `test/realm.json`, and a stand-in for
the authorisation API (`test/stub_authz.py`), then runs `test/drive.py`, which plays the third
party and the customer and checks every behaviour above.

The workflow `.github/workflows/build-keycloak-fapi.yaml` does both on every pull request that
changes this folder or the chart's values, builds the image of `Containerfile`, and checks that
the image holds the tested archive. On `main` it pushes
`ghcr.io/openova-io/openova/keycloak-fapi-consent:<short commit>` and signs it. The archive is
at `/providers/fapi-consent.jar` in the image.

Both providers of the consent step and the PAR endpoint implement SPIs that Keycloak marks
internal (log line `KC-SERVICES0047`). The workflow builds against the version the chart pins, so
a Keycloak upgrade is tested here first.
