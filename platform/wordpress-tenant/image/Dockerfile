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
