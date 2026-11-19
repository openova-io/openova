templates/catalog-seed/blueprints.yaml is GENERATED: python3 scripts/generate-catalog.py writes it, and the
job "regenerate and prove no diff" of .github/workflows/check-catalog-generated-drift.yaml fails a change whose
committed copy differs. Never edit it by hand: change the Blueprint file (platform/<name>/blueprint.yaml or
products/<name>/blueprint.yaml) or a data file, run the generator and commit the result.

It holds one Blueprint object for every Blueprint file that no seed-exclusions.yaml names (platform/_catalog/ and,
for a provider's Blueprint, infra/providers/<provider>/), so /api/v1/catalog lists the catalogue from the first
install on; the catalog client reads these objects through its cluster fallback. The block is gated on
.Values.catalogSeed.enabled and every object carries catalyst.openova.io/managed-by: catalog-seed. How each
field is derived is written at the top of scripts/generate-catalog.py.
