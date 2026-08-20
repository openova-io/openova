# bp-wordpress-tenant runtime image — official WordPress + PostgreSQL PHP driver.
#
# WHY THIS IMAGE EXISTS (#4152, root-caused live on omantel.biz / kom4dc):
# bp-wordpress-tenant runs WordPress against bp-cnpg PostgreSQL (ticket #800),
# NOT MySQL. The pg4wp drop-in (wp-content/db.php) sets DB_DRIVER='pgsql' and
# talks to Postgres via PHP's `pdo_pgsql` / `pgsql` extensions. The official
# `wordpress:6-php8.3-apache` image ships ONLY `mysqli` / `PDO` / `pdo_sqlite`
# — `php -m` there shows NO `pgsql` / `pdo_pgsql`, so every WordPress request
# 500s the moment pg4wp tries to open the Postgres connection (the FINAL
# blocker after PR #4151 drove the pod from CrashLoop to Apache-serving).
#
# This image extends the official WordPress image with the two PG extensions
# using the canonical docker-php ext-deps idiom: it installs `libpq-dev` to
# BUILD the extensions, then walks the freshly-built `pdo_pgsql.so`'s shared-
# object dependencies (via `ldd`) and re-marks the runtime `libpq` package as
# manually-installed BEFORE `apt-get purge --auto-remove` strips the build-only
# `-dev` packages. Without that ldd/dpkg-query dance the auto-remove would also
# evict `libpq5`, breaking the extension at runtime. Same idiom the official
# php/docker library uses for native ext deps.
#
# Built + published to ghcr.io/openova-io/openova/wordpress-tenant-pg by
# .github/workflows/build-bp-wordpress-tenant-pg.yaml on every push touching
# platform/wordpress-tenant/image/**. On a Sovereign the containerd registry
# mirror rewrites ghcr.io -> harbor.openova.io/proxy-ghcr transparently, so the
# chart references the full ghcr path verbatim (NOT via global.imageRegistry —
# see _helpers.tpl bp-wordpress-tenant.wordpressImage host-qualified skip).
FROM wordpress:6-php8.3-apache

RUN set -eux; \
    savedAptMark="$(apt-mark showmanual)"; \
    apt-get update; \
    apt-get install -y --no-install-recommends libpq-dev; \
    docker-php-ext-install -j"$(nproc)" pdo_pgsql pgsql; \
    apt-mark auto '.*' > /dev/null; \
    apt-mark manual $savedAptMark > /dev/null; \
    ldd "$(php -r 'echo ini_get("extension_dir");')"/pdo_pgsql.so \
      | awk '/=>/ { so=$(NF-1); if (index(so, "/usr/local/") == 1) next; gsub("^/(usr/)?","/",so); printf "*%s\n", so }' \
      | sort -u | xargs -r dpkg-query --search 2>/dev/null \
      | cut -d: -f1 | sort -u | xargs -r apt-mark manual > /dev/null; \
    apt-get purge -y --auto-remove -o APT::AutoRemove::RecommendsImportant=false; \
    rm -rf /var/lib/apt/lists/*

# Build-time assertion: fail the image build if the PG driver did not land.
# This is the regression guard for #4152 — a future base-image bump that drops
# the extension can never silently ship.
RUN php -m | grep -E 'pdo_pgsql|^pgsql$'

# ─── Baked third-party WordPress artifacts (#6311, Refs #3988) ───────────────
#
# WHY: before this, BOTH the runtime Deployment's `wp-plugin-install`
# initContainer AND the post-install `oidc-config` hook Job fetched these two
# artifacts over the wire AT INSTALL TIME — from `github.com` and
# `downloads.wordpress.org`. That directly contradicts Pillar 5: cutover
# step-08 (`egress-block-test`) holds a deny-egress NetworkPolicy against
# `github.com`, `ghcr.io` and `harbor.openova.io` for 10 minutes and requires
# the cluster to reconcile green through it. A customer Application that
# reaches GitHub to install cannot exist on a sovereign Sovereign. Live
# symptom before the fix: the `oidc-config` Job CrashLoopBackOff on
# `curl: (28) Connection timed out after 120001 milliseconds`.
#
# WHICH SEAM: this repo mirrors exactly TWO artifact classes into a post-
# cutover Sovereign's local Harbor — OCI **container images** and **Helm
# charts** (cutover step-03 `harbor-prewarm` skopeo-pushes both). There is NO
# mirroring seam for plain file artifacts (no `oras`, no zip/tarball mirror
# anywhere in platform/self-sovereign-cutover/). So the correct reuse is to
# carry these bytes as LAYERS OF AN IMAGE THAT IS ALREADY MIRRORED — and
# `wordpress-tenant-pg` is already in the prewarm set by name (see
# platform/self-sovereign-cutover/chart/Chart.yaml, which calls out
# `wordpress-tenant-pg:<tag>@sha256:…` verbatim as a tag+digest-pinned source).
# No new mechanism is introduced.
#
# Fetching here is BUILD time on a GitHub Actions runner, not install time on
# a Sovereign — the sovereignty contract is about what a Sovereign reaches for
# while converging, and after this change it reaches for nothing.
#
# Consumers copy from /usr/src/openova-wp-artifacts (never from the network):
#   - templates/deployment.yaml         `wp-plugin-install` initContainer
#   - templates/oidc-config-job.yaml    `wp-artifacts` initContainer
# Both are pinned by the image digest in values.yaml, so the artifact versions
# are pinned transitively — no floating ref (docs/PRINCIPLES.md #4).
# Extraction-path notes (kept OUT of the RUN body — a `#` line inside a
# backslash continuation is a Dockerfile parser edge case, not a shell comment):
#   - pg4wp's release tag KEEPS its `v` prefix, while GitHub strips the `v`
#     from the extracted top-level directory (`postgresql-for-wordpress-3.3.1`).
#   - openid-connect-generic's tags are UN-prefixed, so the extracted directory
#     is `openid-connect-generic-<version>`.
#   - The plugin is baked under the BARE `openid-connect-generic` slug because
#     that is the directory name the oidc-config Job's `wp plugin activate`
#     writes into WordPress's `active_plugins` option row. A differently-named
#     runtime directory (the old wordpress.org slug
#     `daggerhart-openid-connect-generic`) leaves that row pointing at a file
#     that does not exist, so the plugin never loads at runtime.
ARG PG4WP_VERSION=3.3.1
ARG OIDC_PLUGIN_REPO=oidc-wp/openid-connect-generic
ARG OIDC_PLUGIN_VERSION=3.11.3
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates curl unzip; \
    mkdir -p /usr/src/openova-wp-artifacts/plugins; \
    curl -fsSL -o /tmp/pg4wp.zip \
      "https://github.com/PostgreSQL-For-Wordpress/postgresql-for-wordpress/archive/refs/tags/v${PG4WP_VERSION}.zip"; \
    unzip -q /tmp/pg4wp.zip -d /tmp; \
    cp -a "/tmp/postgresql-for-wordpress-${PG4WP_VERSION}/pg4wp" /usr/src/openova-wp-artifacts/pg4wp; \
    cp "/tmp/postgresql-for-wordpress-${PG4WP_VERSION}/pg4wp/db.php" /usr/src/openova-wp-artifacts/db.php; \
    curl -fsSL -o /tmp/oidc.zip \
      "https://github.com/${OIDC_PLUGIN_REPO}/archive/refs/tags/${OIDC_PLUGIN_VERSION}.zip"; \
    unzip -q /tmp/oidc.zip -d /tmp; \
    cp -a "/tmp/openid-connect-generic-${OIDC_PLUGIN_VERSION}" \
          /usr/src/openova-wp-artifacts/plugins/openid-connect-generic; \
    printf 'pg4wp=%s\nopenid-connect-generic=%s (%s)\n' \
      "${PG4WP_VERSION}" "${OIDC_PLUGIN_VERSION}" "${OIDC_PLUGIN_REPO}" \
      > /usr/src/openova-wp-artifacts/VERSIONS; \
    rm -rf /tmp/pg4wp.zip /tmp/oidc.zip \
           "/tmp/postgresql-for-wordpress-${PG4WP_VERSION}" \
           "/tmp/openid-connect-generic-${OIDC_PLUGIN_VERSION}"; \
    apt-get purge -y --auto-remove unzip; \
    rm -rf /var/lib/apt/lists/*

# Build-time assertions: fail the image build if a baked artifact is missing,
# empty, or unreadable by the runtime uid. This is the regression guard for
# #6311 — the chart no longer has a network fallback, so an image that silently
# ships without these would break every per-Org WordPress install rather than
# degrade it. The paths asserted are exactly the three the chart copies from.
#
# The second stanza is the one that matters operationally: the artifacts are
# written by root here, but every consumer (the Deployment's
# `wp-plugin-install` init and the oidc-config Job's `wp-artifacts` init) runs
# as uid 33 under a dropped-ALL-capabilities securityContext. Asserting that
# uid 33 can actually READ and COPY them is a positive proof; a blanket
# `chmod -R a+rX` would have been an assumption dressed as a fix — and would
# also have been the only step in this file that cannot run under a rootless
# builder.
RUN set -eux; \
    test -s /usr/src/openova-wp-artifacts/db.php; \
    test -s /usr/src/openova-wp-artifacts/pg4wp/driver_pgsql.php; \
    test -s /usr/src/openova-wp-artifacts/plugins/openid-connect-generic/openid-connect-generic.php
RUN set -eux; \
    mkdir -p /tmp/uid33probe; \
    chown 33:33 /tmp/uid33probe; \
    su -s /bin/sh -c 'set -eu; \
      cp -R /usr/src/openova-wp-artifacts/pg4wp /tmp/uid33probe/pg4wp; \
      cp -R /usr/src/openova-wp-artifacts/plugins/openid-connect-generic /tmp/uid33probe/oidc; \
      cp /usr/src/openova-wp-artifacts/db.php /tmp/uid33probe/db.php' www-data; \
    test -s /tmp/uid33probe/db.php; \
    test -s /tmp/uid33probe/pg4wp/driver_pgsql.php; \
    test -s /tmp/uid33probe/oidc/openid-connect-generic.php; \
    rm -rf /tmp/uid33probe
