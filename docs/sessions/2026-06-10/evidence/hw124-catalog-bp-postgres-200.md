# hw124 catalog data-GET — bp-postgres now 200 (was 404)

Captured 2026-06-10 on hw124 (`90a98a78`), chart `bp-catalyst-platform` 1.4.552→1.4.553.
Closes the gap flagged this session: #3191 added the PostgreSQL App-card to
`blueprints.json` but NOT to the in-cluster catalog-seed the API actually serves,
so `GET /api/v1/catalog/bp-postgres` returned 404 while all siblings resolved.
Fixed by #3199 (catalog-seed CR) + unwedged by #3200/#3201.

Token minted in-cluster from Secret `catalyst-system/catalyst-kc-sa-credentials`
(client `catalyst-api-server`, realm `sovereign`); GET against
`catalyst-api.catalyst-system.svc:8080`.

```
GET /api/v1/catalog/bp-postgres -> 200   (was 404)
GET /api/v1/catalog/bp-grafana  -> 200
GET /api/v1/catalog/bp-cnpg     -> 200
GET /api/v1/catalog/bp-does-not-exist -> 404   (control — proves 200 is a real hit)

payload: {"name":"bp-postgres","version":"0.1.1","visibility":"listed",
          "card":{"title":"PostgreSQL","category":"data", ...}}

kubectl get blueprints.catalyst.openova.io -A --no-headers | wc -l  -> 78  (was 77; bp-postgres added)
kubectl get blueprints.catalyst.openova.io bp-postgres
  -> name=bp-postgres version=0.1.1 visibility=listed title=PostgreSQL
```

Scope: this is the catalog **data layer** (API serves the bp-postgres card).
The **UI class-page render** (`console → Catalog → /catalog/bp-postgres`) is still
pending an operator console session — the headless agent hits the PIN-login wall.
