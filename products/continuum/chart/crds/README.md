# Continuum CRD

The `Continuum.dr.openova.io/v1` CRD lives at
`products/catalyst/chart/crds/continuum.yaml` and is shipped via the
Catalyst platform chart (slice B8 of EPIC-0, merged via #1110).

It is **NOT duplicated here** — duplicating CRDs across charts causes
the helm-controller / kustomize-controller ownership flap that bit
sovereign-wildcard-tls (see `products/catalyst/chart/values.yaml`
comment on `parentZones`).

If a future change makes Continuum installable on a cluster that
doesn't run the Catalyst chart, that's the moment to extract the CRD
into a shared location (e.g. a `bp-openova-crds` chart) — NOT to
duplicate it here.

This directory exists only to make the layout obvious to operators who
read `helm show crds bp-continuum`. The empty `crds/` is intentional.

— K-Cont-1 (#1101)
