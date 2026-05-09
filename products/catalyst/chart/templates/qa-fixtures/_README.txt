QA-loop fixtures (qa-loop iter-6 Cluster-F).

These templates seed the test-matrix fixtures the qa-loop expects on
every Sovereign that participates in QA testing:

  - Namespace `qa-omantel` (env-type=dev, application=qa-wp)
  - ConfigMap `disposable-cm` and Secret `qa-wp-creds` in qa-omantel
  - UserAccess `qa-user1` (cluster-scoped logical principal) in
    catalyst-system, tier-developer + scopes {env-type=dev,
    application=qa-wp, organization=omantel-platform}
  - RoleBinding `qa-user1-developer` in qa-omantel
  - Blueprint `bp-qa-custom` (cluster-scoped, version 0.1.0)

Default-OFF gate: set `qaFixtures.enabled: true` on test Sovereigns
only. Production Sovereigns must keep the default `false` value so
test fixtures never leak into customer clusters.

Per founder rule (qa-loop iter-6 Cluster-F): the qa-omantel matrix
asserts behavior against fixture resources, and the seeder belongs
on the chart side so a fresh-provisioned Sovereign reaches the same
state as the ad-hoc operator-applied resources on the live omantel
chroot.

Naming note: the namespace name is `qa-omantel` (with the omantel
suffix) because the matrix was authored against omantel's cluster.
For other Sovereigns wiring qa-loop, override
`qaFixtures.namespace` accordingly — there is nothing in the seeder
that hard-codes omantel.
