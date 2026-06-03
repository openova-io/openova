> NOTE (2026-06-03): this whole subdir is pending migration into docs/RUNBOOKS.md (§3 chart authoring + §7 troubleshooting) per lean-doc strategy. The entries below are genuinely-unique operational debugging knowledge not yet duplicated in the canonical 7 docs — fold them in, then archive this subdir.

# Lessons Learned

Operational knowledge discovered during platform development. Platform/infrastructure behaviors that exist regardless of our code; non-obvious config or behavior found during debugging; patterns that would bite the next contributor.

Organized by domain.

| Domain | What's in it |
|---|---|
| [helm-controller-rbac.md](helm-controller-rbac.md) | Flux helm-controller v1.1.0 RBAC + template-parse quirks |
| [helm-controller-logs.md](helm-controller-logs.md) | Flux v2.4 helm-controller stdout uses nested-object JSON for HelmRelease, not flat strings |
| [chi-router-quirks.md](chi-router-quirks.md) | go-chi does not decode `%3A` (and other path-safe specials) before route matching |
| [helm-hooks-and-crd-ordering.md](helm-hooks-and-crd-ordering.md) | `before-hook-creation` deadlocks on first install when the CRD comes from the same chart's upstream subchart — architectural fix is chart-split + Flux dependsOn |
| [catalyst-bootstrap-api.md](catalyst-bootstrap-api.md) | `tofu destroy` works against the on-disk workdir without re-prompting credentials — destructive endpoints split tofu vs Hetzner-direct paths cleanly |
| [keycloak-realm-import-and-chart-tests.md](keycloak-realm-import-and-chart-tests.md) | Keycloak `varchar(255)` DESCRIPTION cap; `--show-only` multi-doc render trap; `gitea admin auth list --vertical-bars` tab-padding; `sovereign-fqdn` key contract |
