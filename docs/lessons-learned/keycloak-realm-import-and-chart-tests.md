> NOTE (2026-06-03): pending migration into docs/RUNBOOKS.md §3 (chart authoring) + §7 (troubleshooting) per lean-doc strategy.

# Keycloak realm-import + chart-test gotchas

Patterns surfaced during the G117.E2E-A1 hw86 stabilization cascade (issue #2816, 2026-06-03). Six related breakages chased over a single session — capturing the platform-level invariants so the next contributor catches them before they reach a live Sovereign.

## Keycloak `public.CLIENT.DESCRIPTION` is `varchar(255)`

Keycloak's relational schema (Postgres + the bundled MariaDB/H2) defines `public.CLIENT.DESCRIPTION` as `varchar(255)`. The `keycloak-config-cli` realm-import Job opens a JDBC batch with the full description string; any single >255-char description aborts the entire batch with:

```
PSQLException: ERROR: value too long for type character varying(255)
Batch entry 0 update public.CLIENT set ... DESCRIPTION=('...'), ...
```

The post-install / post-upgrade Job exhausts `backoffLimit`, Helm rolls back the bp-keycloak HR, and **every downstream HR with a `dependsOn: bp-keycloak`** stalls Ready=False on `dependency 'flux-system/bp-keycloak' is not ready`. This is a cluster-wide blast radius from a single overly-wordy JSON-block comment.

Same anti-pattern was previously caught for `catalyst-api-server` (PR #1285, 2026-05-15). It re-emerged for two Tier-2 clients added in PR #2802 (`powerdns-admin` 283 chars + `hubble-ui` 259 chars) — and for tenant-realm + per-Org-realm client descriptions in PR #2827.

**Rule**: every `"description":` field in `platform/keycloak/chart/templates/configmap-*-realm.yaml` must be ≤255 chars. Push implementation rationale into the YAML comment ABOVE the JSON block (unbounded), not into the JSON body. The chart test `platform/keycloak/chart/tests/g117-e2e-realm-client-description-255-cap.sh` re-asserts the cap at render time across `clients[]`, `authenticationFlows[]`, and `authenticatorConfig[]` and warns on entries >200 chars to surface drift early. PR #2827 extended the same test to tenant-realm + per-Org-realm templates.

**Ref**: #2816 #2802 #2827 #1285

## Chart-test `--show-only` returns the WHOLE multi-doc template, not just the matching kind

`helm template ... --show-only templates/foo.yaml` emits **every** document in `foo.yaml`, not just the first or only the requested kind. Both PR #2789 (G117.5b secret-rotation salt test) and PR #2806 hit this — `yq -r '.data["realm.json"]'` was applied across BOTH the ConfigMap (with the realm JSON) and a sibling Secret (with no `.data["realm.json"]`), producing `null\n---\nnull`, and the downstream `jq` aborted with `parse error: Invalid numeric literal at line 3, column 0`.

The chart test silently exited 5 and the Blueprint Release was blocked publishing bp-keycloak 1.4.14 to GHCR — which in turn blocked the entire hw86 cascade because every Sovereign was pinned to a non-existent chart.

**Rule**: in chart tests, always `select(.kind == "<ExpectedKind>")` before extracting `.data["<key>"]`. Defensive even when today the template emits only one kind — refactors that add a sibling Secret/ConfigMap will silently break the test. Also: the ConfigMap data key in the sovereign-realm config is `sovereign-realm.json`, NOT `realm.json` — confirm the key against the actual template, don't guess from sibling tests.

**Ref**: #2816 #2789 #2806 #2817

## `gitea admin auth list --vertical-bars` pads cells with TABS, not spaces

The Gitea CLI command `gitea admin auth list --vertical-bars` documents itself as `|`-separated, but each field is left-padded with TABs to align the columns. An awk one-liner that strips only spaces (`gsub(/ /, "", $1)`) leaves a trailing `\t` in the extracted ID:

```
ID    |Name           |Type   |Enabled
1     |openova-sso    |OAuth2 |true
↑     ↑               ↑       ↑
TABs everywhere
```

Result: `AUTH_ID="1\t"`, and the next call `gitea admin auth update-oauth --id "1\t"` aborts with `invalid value "1\t" for flag -id: parse error`. The bp-gitea SSO-configure post-upgrade Job exhausts backoffLimit and rolls back the whole bp-gitea HR — blocking bp-catalyst-platform, bp-self-sovereign-cutover, bp-sso-bridge, and bp-continuum on `dependency 'flux-system/bp-gitea' is not ready`.

The bug was long-dormant: PR #2821 (resolving the live Pod by label selector instead of `gitea-0`) made the Job actually execute on Deployment-mode Gitea, which then exposed the AUTH_ID parsing failure.

**Rule**: when extracting tab-aligned columnar output from any CLI, use `gsub(/[[:space:]]+/, "", $field)` — strips spaces, tabs, and CR in one pass. Chart test `platform/gitea/chart/tests/g117-e2e-a1-auth-id-tab-strip.sh` re-asserts the pattern on a representative `--vertical-bars` sample AND on the live chart template so the unit-test stays anchored to the script.

**Ref**: #2816 #2821 #2826

## `sovereign-fqdn` ConfigMap data key is `fqdn`, not `sovereignFqdn`

`bp-catalyst-platform` emits the per-Sovereign `sovereign-fqdn` ConfigMap from `products/catalyst/chart/templates/sovereign-fqdn-configmap.yaml`. The canonical data key is **`fqdn`** (literal YAML map key, not a templated value). The bp-sso-bridge reconciler reads it via jsonpath `{.data.${SOVEREIGN_FQDN_SOVEREIGN_FQDN_KEY}}` — and the chart default for that env var has been `sovereignFqdn` (NOT `fqdn`) since G91.1 (PR #2683, 2026-05-27).

Result: the reconciler had been a **silent no-op on every Sovereign for ~7 days**. Every tick logged `WARN: sovereign-fqdn ConfigMap missing; skipping tick` and the Tier-1 OpenBao writes for `sso/<app>` + the per-Org realm reconciliation never happened. The bp-gitea + bp-grafana post-install hooks waited for `ExternalSecret`s backed by empty OpenBao paths and timed out — a single-character key-name typo in `values.yaml` cascaded into a SSO-stack-wide outage with no obvious blame.

PR #2820 (G117.E2E-C1) fixed the values.yaml default + added a cross-chart contract guard test (`platform/sso-bridge/chart/tests/g117-e2e-a1-sovereign-fqdn-key-contract.sh`) that compares the chart default side-by-side against the emitter key extracted from the catalyst-platform template. Either-side drift fails CI.

**Rule**: any time a chart-consumer reads a ConfigMap data key by name, add a cross-chart contract test that pins the consumer's default to the emitter's literal key. Don't assume reviewer attention catches "1 silent log line per 6 minutes" — it didn't here, for a week. The test is 50 lines and forever-running.

**Ref**: #2816 #2820 #2683
