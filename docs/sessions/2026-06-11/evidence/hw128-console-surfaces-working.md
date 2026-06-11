# hw128 — sovereign operator console fully working (live walk 2026-06-11)

Logged into console.hw128.omani.works via real email+PIN SSO. Three surfaces walked + screenshotted:

1. **Dashboard** — resource treemap, 101 items, all components (keycloak/harbor/gitea/powerdns/dmz-mgmt-rtz vclusters/cnpg/…), "E" logged in. Did NOT crash (#3281 is provisioning-only; converged env renders).
2. **Jobs** (hw128-jobs-both-regions-PASS.png) — Region column + filter; me-east-215-a (16) + me-east-215-b-1 (690) rows. #3278 PASS.
3. **Apps** (hw128-apps-page.png) — Deployments 49 / Catalog 63 tabs; all bootstrap apps INSTALLED with categories + dependency graphs (Gitea→keycloak+gateway-api+cnpg, Grafana→cnpg+loki+mimir, OpenBao→spire+gateway-api+cnpg).

Plus grafana SSO-into-app PASS (hw128-grafana-sso-PASS-landed-in-app.png, #3272).

hw128 = first clean zero-touch 2-region Sovereign of the session, 57/60 HRs Ready, fully walkable.
