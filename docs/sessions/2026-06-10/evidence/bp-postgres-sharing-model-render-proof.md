# bp-postgres shareable App-card model — render proof (ADR-0010)

Confirms the reusable/shareable backing-services model renders the founder's
"N consumers share one Postgres instance" case. Rendered `platform/postgres/chart`
with the CNPG CRDs present (`--api-versions postgresql.cnpg.io/v1[/Cluster|/Database]`)
and a 1-instance / 2-consumer `databases[]`:

```
databases:
  - name: giteadb,  owner: gitea,  reflect: {secretName: gitea-pg-app,  namespaces: [gitea]}
  - name: harbordb, owner: harbor, reflect: {secretName: harbor-pg-app, namespaces: [harbor]}
```

Rendered objects (the ADR-0010 3-object model):
```
1 × Cluster    shared-pg                       (the engine = ONE App-card instance)
2 × Database    shared-pg-giteadb, shared-pg-harbordb   (isolated DB per consumer)
2 × managed role (ensure: present)              (per-consumer login role on the Cluster)
2 × Secret      gitea-pg-app  -> reflects postgres/shared-pg-gitea
                harbor-pg-app -> reflects postgres/shared-pg-harbor   (consumer-ns externalDatabase creds)
```

So one bp-postgres instance with two `databases[]` entries = 1 shared CNPG Cluster
+ 2 isolated databases + 2 roles + 2 reflected connection Secrets — pure declarative
YAML (CNPG `Database` CR + `managed.roles` + bp-reflector), NO custom controller and
NO Crossplane. Ref-counting = the Flux dependency graph (each consumer `dependsOn` the
instance). The 3+3+1 wireframe = three such instances with 3/3/1 `databases[]` entries.

The catalog App-card for the instance is bp-postgres (visibility: listed, #3199, live 200).
The full LIVE demo (operator sees N instances + a Consumers table; 7 apps sharing 3
instances) requires a prov with SOVEREIGN_ENABLE_SHARED_PG=true — gated off by default
(safe-by-default), so it needs a fresh prov to walk.
