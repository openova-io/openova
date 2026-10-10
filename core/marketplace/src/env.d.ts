/// <reference types="astro/client" />

interface ImportMetaEnv {
  /**
   * #6971 — build-time override for the Catalyst BSS (chargeback) base URL the
   * storefront reads its package table from, e.g. `http://127.0.0.1:18977` for
   * a local stub. Normally UNSET: the shipped bundle is the same on every
   * Sovereign and derives `https://chargeback.<fqdn>` from its own
   * `marketplace.<fqdn>` host at runtime (src/lib/config.ts::chargebackBaseURL).
   */
  readonly PUBLIC_CHARGEBACK_URL?: string;
}
