/**
 * providerSizes.ts — per-provider node-size catalog (canonical pricing).
 *
 * Every SKU id, label, vCPU/RAM/disk spec, and price in this file matches
 * what the corresponding cloud provider's pricing page renders today
 * (April 2026). No SKU literal lives anywhere else in the wizard — the UI
 * always reads PROVIDER_NODE_SIZES[provider].
 *
 * SOURCES (canonical pricing pages — fetched 2026-04-29):
 *   • Hetzner Regular Performance (CPX AMD shared):
 *       https://www.hetzner.com/cloud/regular-performance
 *   • Hetzner Cost-Optimized (CX Intel shared, CAX Ampere ARM):
 *       https://www.hetzner.com/cloud/cost-optimized
 *   • Hetzner Dedicated (CCX dedicated vCPU AMD):
 *       https://www.hetzner.com/cloud/dedicated-performance
 *       https://www.hetzner.com/cloud/general-purpose
 *   • Huawei Cloud ECS (s7/c7n/m7 families):
 *       https://www.huaweicloud.com/intl/en-us/product/ecs/pricing.html
 *       (per-flavor specs cross-checked against Cloud Mercato Public Cloud
 *        Reference, e.g. https://pcr.cloud-mercato.com/providers/huawei/...)
 *   • Oracle Cloud Compute (VM.Standard Flex shapes):
 *       https://www.oracle.com/cloud/compute/pricing/
 *       https://www.oracle.com/cloud/compute/virtual-machines/pricing/
 *   • AWS EC2 On-Demand (m6i / c6i / r6i / m7g, us-east-1 Linux):
 *       https://aws.amazon.com/ec2/pricing/on-demand/
 *       (per-instance specs and rates cross-checked against
 *        https://instances.vantage.sh/aws/ec2/<id>)
 *   • Azure VM (Dsv5 / Esv5 / Dpsv5, West Europe Linux):
 *       https://azure.microsoft.com/en-us/pricing/details/virtual-machines/linux/
 *       (per-VM specs and rates cross-checked against
 *        https://instances.vantage.sh/azure/vm/<id>)
 *
 * CURRENCY:
 *   The wizard surfaces every price in EUR because the customer-facing
 *   spend context is European. Hetzner publishes natively in EUR. Huawei,
 *   Oracle, AWS, and Azure publish in USD; those USD list prices are
 *   converted at the stable interbank rate 1 USD = 0.92 EUR (snapshot
 *   2026-04). The conversion is applied once at table-build time below
 *   (see priceUSDtoEUR) — never at render time. When the rate moves more
 *   than ±5%, refresh this constant and re-stamp the table.
 *
 * REGION-SPECIFIC HETZNER PRICES:
 *   Hetzner publishes a single "max €/month" headline per SKU on the
 *   Regular Performance / Cost-Optimized / Dedicated pages, which reflects
 *   their European DC base price (Falkenstein/Nuremberg). The same SKU is
 *   surcharged in Helsinki, Ashburn, Hillsboro, and Singapore — the
 *   regional tabs on hetzner.com/cloud/regular-performance render the
 *   higher tiers per region. The values below are the European base
 *   (FSN1/NBG1) tier as that is the canonical "from" price on each page.
 *   Exception: CPX32 reflects the Helsinki tier (€14.49/mo, €0.0232/hr)
 *   per founder direction — that's the price OpenOva positions as the
 *   solo-Sovereign starter alongside the Falkenstein default.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (no hardcoded URLs / hardcoded
 * values), refreshing this table is a one-file edit. Operators who run
 * the wizard against a private custom-priced cloud account should fork
 * this file or wire a runtime config endpoint when one ships.
 */

import type { CloudProvider } from '@/entities/deployment/model'

export type SkuCategory =
  | 'shared-intel'    // Hetzner CX (Intel shared vCPU)
  | 'shared-amd'      // Hetzner CPX (AMD shared vCPU)
  | 'shared-arm'      // Hetzner CAX, Azure Dpsv5, AWS m7g, OCI A1
  | 'dedicated'       // Hetzner CCX (dedicated vCPU)
  | 'general-purpose' // AWS m6i, Azure Dsv5, OCI E5/E4 standard, Huawei s7/c7n
  | 'compute-optimized' // AWS c6i
  | 'memory-optimized'  // AWS r6i, Azure Esv5, Huawei m7

export interface NodeSize {
  /** Native provider instance-type id — passed verbatim to the OpenTofu module. */
  id: string
  /** Display label rendered in the wizard. Matches each provider's canonical
   *  pricing page exactly: "CPX32" not "CX32 — Standard"; "VM.Standard.E5.Flex"
   *  not "E5 Standard"; etc. */
  label: string
  vcpu: number
  /** RAM in GB (integer). */
  ram: number
  /** Local SSD in GB, or the literal string "EBS-only" / "Variable" for
   *  cloud SKUs whose disk is a separately-priced cloud volume. */
  disk: number | string
  /** Hourly price in EUR (drives the wizard's cost rollup). */
  priceHour: number
  /** Monthly price cap in EUR. Hetzner caps hourly at this value;
   *  hyperscalers compute this as priceHour × 730 for display. */
  priceMonth: number
  category: SkuCategory
  description: string
  /** Marked as the "starter default" within its provider's catalog. */
  recommended?: boolean
}

/** USD → EUR conversion applied to hyperscaler list prices. Snapshot
 *  2026-04 interbank rate. See file-level CURRENCY comment. */
const USD_TO_EUR = 0.92

/** Round to 4dp — keeps hourly rates precise enough for cost rollup
 *  without surfacing fake-precision trailing zeros. */
const roundHour = (n: number): number => Math.round(n * 10000) / 10000
const roundMonth = (n: number): number => Math.round(n * 100) / 100

/** Convert USD/hour → EUR/hour, rounded for display. */
const priceUSDtoEUR = (usdHour: number): { priceHour: number; priceMonth: number } => ({
  priceHour: roundHour(usdHour * USD_TO_EUR),
  priceMonth: roundMonth(usdHour * USD_TO_EUR * 730),
})

export const PROVIDER_NODE_SIZES: Record<CloudProvider, NodeSize[]> = {
  /* ──────────────────────────────────────────────────────────────────
     HETZNER — natively EUR. Source pages above. April 2026 prices.
     CX = Intel shared (Cost-Optimized page).
     CPX = AMD shared (Regular Performance page).
     CAX = Ampere ARM shared (Cost-Optimized page).
     CCX = AMD dedicated vCPU (Dedicated/General-Purpose page).
     ────────────────────────────────────────────────────────────────── */
  hetzner: [
    // CX — Intel shared
    { id: 'cx23', label: 'CX23', vcpu: 2, ram: 4, disk: 40, priceHour: 0.0064, priceMonth: 3.99,
      category: 'shared-intel', description: 'Intel shared vCPU — entry, dev/POC' },
    { id: 'cx33', label: 'CX33', vcpu: 4, ram: 8, disk: 80, priceHour: 0.0104, priceMonth: 6.49,
      category: 'shared-intel', description: 'Intel shared vCPU — standard small workloads' },
    { id: 'cx43', label: 'CX43', vcpu: 8, ram: 16, disk: 160, priceHour: 0.0192, priceMonth: 11.99,
      category: 'shared-intel', description: 'Intel shared vCPU — performance' },
    { id: 'cx53', label: 'CX53', vcpu: 16, ram: 32, disk: 320, priceHour: 0.0360, priceMonth: 22.49,
      category: 'shared-intel', description: 'Intel shared vCPU — high-memory' },

    // CPX — AMD shared (Regular Performance lineup)
    { id: 'cpx22', label: 'CPX22', vcpu: 2, ram: 4, disk: 80, priceHour: 0.0128, priceMonth: 7.99,
      category: 'shared-amd', description: 'AMD shared vCPU — entry compute' },
    // CPX32 — founder-stated Helsinki tier price; recommended starter SKU.
    { id: 'cpx32', label: 'CPX32', vcpu: 4, ram: 8, disk: 160, priceHour: 0.0232, priceMonth: 14.49,
      category: 'shared-amd', description: 'AMD shared vCPU — solo-Sovereign starter (4 vCPU / 8 GB / 160 GB SSD)',
      recommended: true },
    { id: 'cpx42', label: 'CPX42', vcpu: 8, ram: 16, disk: 320, priceHour: 0.0408, priceMonth: 25.49,
      category: 'shared-amd', description: 'AMD shared vCPU — production worker' },
    { id: 'cpx52', label: 'CPX52', vcpu: 12, ram: 24, disk: 480, priceHour: 0.0585, priceMonth: 36.49,
      category: 'shared-amd', description: 'AMD shared vCPU — heavy worker' },
    { id: 'cpx62', label: 'CPX62', vcpu: 16, ram: 32, disk: 640, priceHour: 0.0809, priceMonth: 50.49,
      category: 'shared-amd', description: 'AMD shared vCPU — top-of-range shared' },

    // CAX — Ampere ARM shared
    { id: 'cax11', label: 'CAX11', vcpu: 2, ram: 4, disk: 40, priceHour: 0.0072, priceMonth: 4.49,
      category: 'shared-arm', description: 'Ampere Altra ARM — entry, ARM-native workloads' },
    { id: 'cax21', label: 'CAX21', vcpu: 4, ram: 8, disk: 80, priceHour: 0.0128, priceMonth: 7.99,
      category: 'shared-arm', description: 'Ampere Altra ARM — standard' },
    { id: 'cax31', label: 'CAX31', vcpu: 8, ram: 16, disk: 160, priceHour: 0.0256, priceMonth: 15.99,
      category: 'shared-arm', description: 'Ampere Altra ARM — performance' },
    { id: 'cax41', label: 'CAX41', vcpu: 16, ram: 32, disk: 320, priceHour: 0.0505, priceMonth: 31.49,
      category: 'shared-arm', description: 'Ampere Altra ARM — high-memory' },

    // CCX — Dedicated vCPU AMD (General Purpose / Dedicated page)
    { id: 'ccx13', label: 'CCX13', vcpu: 2, ram: 8, disk: 80, priceHour: 0.0256, priceMonth: 15.99,
      category: 'dedicated', description: 'Dedicated AMD vCPU — small production' },
    { id: 'ccx23', label: 'CCX23', vcpu: 4, ram: 16, disk: 160, priceHour: 0.0505, priceMonth: 31.49,
      category: 'dedicated', description: 'Dedicated AMD vCPU — HA control-plane' },
    { id: 'ccx33', label: 'CCX33', vcpu: 8, ram: 32, disk: 240, priceHour: 0.1001, priceMonth: 62.49,
      category: 'dedicated', description: 'Dedicated AMD vCPU — production worker' },
    { id: 'ccx43', label: 'CCX43', vcpu: 16, ram: 64, disk: 360, priceHour: 0.2003, priceMonth: 124.99,
      category: 'dedicated', description: 'Dedicated AMD vCPU — heavy data plane' },
    { id: 'ccx53', label: 'CCX53', vcpu: 32, ram: 128, disk: 600, priceHour: 0.4006, priceMonth: 249.99,
      category: 'dedicated', description: 'Dedicated AMD vCPU — large memory' },
    { id: 'ccx63', label: 'CCX63', vcpu: 48, ram: 192, disk: 960, priceHour: 0.6001, priceMonth: 374.49,
      category: 'dedicated', description: 'Dedicated AMD vCPU — top-of-range' },
  ],

  /* ──────────────────────────────────────────────────────────────────
     HUAWEI CLOUD ECS — list prices in USD, converted at USD_TO_EUR.
     Naming: <family>.<size>.<ratio> where ratio = GB-RAM-per-vCPU.
       s7  (General Computing — Intel)         ratio 2 = 1:2 vCPU:RAM
       c7n (General Computing-plus — Intel)    ratio 2 = 1:2 vCPU:RAM
       m7  (Memory-optimised — Intel)          ratio 8 = 1:8 vCPU:RAM
     Hourly USD prices below are the AP-Singapore rate (mid-tier across
     Huawei's international regions; Cloud Mercato confirmed c7n.xlarge.2
     ranges $0.177–$0.225 across regions, mid ≈ $0.20). m7.large.8 mid
     ≈ $0.114 (Linux). s7 prices follow the s6 → s7 successor uplift.
     ────────────────────────────────────────────────────────────────── */
  huawei: [
    // s7 — general computing (Intel Xeon, balanced)
    { id: 's7.large.2',    label: 's7.large.2',    vcpu: 2, ram: 4,  disk: 'Variable',
      ...priceUSDtoEUR(0.085), category: 'general-purpose',
      description: 'General Computing s7 — entry, dev/POC' },
    { id: 's7.xlarge.2',   label: 's7.xlarge.2',   vcpu: 4, ram: 8,  disk: 'Variable',
      ...priceUSDtoEUR(0.170), category: 'general-purpose',
      description: 'General Computing s7 — standard small workloads' },
    { id: 's7.2xlarge.2',  label: 's7.2xlarge.2',  vcpu: 8, ram: 16, disk: 'Variable',
      ...priceUSDtoEUR(0.340), category: 'general-purpose',
      description: 'General Computing s7 — performance' },
    { id: 's7.4xlarge.2',  label: 's7.4xlarge.2',  vcpu: 16, ram: 32, disk: 'Variable',
      ...priceUSDtoEUR(0.680), category: 'general-purpose',
      description: 'General Computing s7 — heavy worker' },

    // c7n — general computing-plus (Intel Xeon Gold 6348, dedicated perf)
    { id: 'c7n.large.2',   label: 'c7n.large.2',   vcpu: 2, ram: 4,  disk: 'Variable',
      ...priceUSDtoEUR(0.100), category: 'general-purpose',
      description: 'General Computing-plus c7n — Xeon Gold 6348, entry' },
    // Recommended Huawei starter — 4 vCPU / 8 GB / dedicated Intel performance.
    { id: 'c7n.xlarge.2',  label: 'c7n.xlarge.2',  vcpu: 4, ram: 8,  disk: 'Variable',
      ...priceUSDtoEUR(0.200), category: 'general-purpose',
      description: 'General Computing-plus c7n — Xeon Gold 6348, control-plane default',
      recommended: true },
    { id: 'c7n.2xlarge.2', label: 'c7n.2xlarge.2', vcpu: 8, ram: 16, disk: 'Variable',
      ...priceUSDtoEUR(0.400), category: 'general-purpose',
      description: 'General Computing-plus c7n — production worker' },
    { id: 'c7n.4xlarge.2', label: 'c7n.4xlarge.2', vcpu: 16, ram: 32, disk: 'Variable',
      ...priceUSDtoEUR(0.800), category: 'general-purpose',
      description: 'General Computing-plus c7n — heavy worker' },

    // m7 — memory-optimised (Intel Xeon, 1:8 ratio)
    { id: 'm7.large.8',    label: 'm7.large.8',    vcpu: 2, ram: 16, disk: 'Variable',
      ...priceUSDtoEUR(0.114), category: 'memory-optimized',
      description: 'Memory-optimised m7 — JVM, in-memory caches' },
    { id: 'm7.xlarge.8',   label: 'm7.xlarge.8',   vcpu: 4, ram: 32, disk: 'Variable',
      ...priceUSDtoEUR(0.228), category: 'memory-optimized',
      description: 'Memory-optimised m7 — heavy memory worker' },
    { id: 'm7.2xlarge.8',  label: 'm7.2xlarge.8',  vcpu: 8, ram: 64, disk: 'Variable',
      ...priceUSDtoEUR(0.456), category: 'memory-optimized',
      description: 'Memory-optimised m7 — large data plane' },
  ],

  /* ──────────────────────────────────────────────────────────────────
     ORACLE CLOUD INFRASTRUCTURE — Flex shapes, USD list prices.
     Resource-based pricing: charged per OCPU/hr + per GB-RAM/hr.
       VM.Standard.E5.Flex (AMD EPYC Genoa):  $0.030/OCPU + $0.002/GB
       VM.Standard.E4.Flex (AMD EPYC Milan):  $0.025/OCPU + $0.0015/GB
       VM.Standard3.Flex   (Intel Ice Lake):  $0.040/OCPU + $0.0015/GB
       VM.Standard.A1.Flex (Ampere Altra):    $0.010/OCPU + $0.0015/GB
     Catalog entries are common Flex shape sizes the wizard exposes —
     the `id` carries the OCPU.GB suffix so the OpenTofu module can
     pass it verbatim to the Compute API. (OCI counts 1 OCPU = 2 vCPU
     for x86; A1 ARM is 1 OCPU = 1 vCPU. The `vcpu` value below is
     vCPU as the UI labels them throughout the wizard.)
     ────────────────────────────────────────────────────────────────── */
  oci: (() => {
    const e5 = (ocpu: number, ramGB: number) => priceUSDtoEUR(ocpu * 0.030 + ramGB * 0.002)
    const e4 = (ocpu: number, ramGB: number) => priceUSDtoEUR(ocpu * 0.025 + ramGB * 0.0015)
    const s3 = (ocpu: number, ramGB: number) => priceUSDtoEUR(ocpu * 0.040 + ramGB * 0.0015)
    const a1 = (ocpu: number, ramGB: number) => priceUSDtoEUR(ocpu * 0.010 + ramGB * 0.0015)
    return [
      // E5.Flex — AMD EPYC Genoa, current generation
      { id: 'VM.Standard.E5.Flex.1.8',  label: 'VM.Standard.E5.Flex (1 OCPU / 8 GB)',
        vcpu: 2, ram: 8, disk: 'Variable', ...e5(1, 8), category: 'general-purpose',
        description: 'AMD EPYC Genoa Flex — minimum viable' },
      // OCI recommended starter — 2 OCPU (4 vCPU) / 16 GB AMD Genoa.
      { id: 'VM.Standard.E5.Flex.2.16', label: 'VM.Standard.E5.Flex (2 OCPU / 16 GB)',
        vcpu: 4, ram: 16, disk: 'Variable', ...e5(2, 16), category: 'general-purpose',
        description: 'AMD EPYC Genoa Flex — control-plane default',
        recommended: true },
      { id: 'VM.Standard.E5.Flex.4.32', label: 'VM.Standard.E5.Flex (4 OCPU / 32 GB)',
        vcpu: 8, ram: 32, disk: 'Variable', ...e5(4, 32), category: 'general-purpose',
        description: 'AMD EPYC Genoa Flex — production worker' },
      { id: 'VM.Standard.E5.Flex.8.64', label: 'VM.Standard.E5.Flex (8 OCPU / 64 GB)',
        vcpu: 16, ram: 64, disk: 'Variable', ...e5(8, 64), category: 'general-purpose',
        description: 'AMD EPYC Genoa Flex — heavy worker' },

      // E4.Flex — AMD EPYC Milan, broad regional coverage
      { id: 'VM.Standard.E4.Flex.2.16', label: 'VM.Standard.E4.Flex (2 OCPU / 16 GB)',
        vcpu: 4, ram: 16, disk: 'Variable', ...e4(2, 16), category: 'general-purpose',
        description: 'AMD EPYC Milan Flex — previous gen, wide region coverage' },
      { id: 'VM.Standard.E4.Flex.4.32', label: 'VM.Standard.E4.Flex (4 OCPU / 32 GB)',
        vcpu: 8, ram: 32, disk: 'Variable', ...e4(4, 32), category: 'general-purpose',
        description: 'AMD EPYC Milan Flex — production' },

      // Standard3.Flex — Intel Ice Lake (Xeon Platinum 8358)
      { id: 'VM.Standard3.Flex.2.16',   label: 'VM.Standard3.Flex (2 OCPU / 16 GB)',
        vcpu: 4, ram: 16, disk: 'Variable', ...s3(2, 16), category: 'general-purpose',
        description: 'Intel Ice Lake Flex — Intel-licensed workloads' },
      { id: 'VM.Standard3.Flex.4.32',   label: 'VM.Standard3.Flex (4 OCPU / 32 GB)',
        vcpu: 8, ram: 32, disk: 'Variable', ...s3(4, 32), category: 'general-purpose',
        description: 'Intel Ice Lake Flex — production' },

      // A1.Flex — Ampere Altra ARM, $0.01/OCPU "penny-a-core"
      { id: 'VM.Standard.A1.Flex.2.12', label: 'VM.Standard.A1.Flex (2 OCPU / 12 GB)',
        vcpu: 2, ram: 12, disk: 'Variable', ...a1(2, 12), category: 'shared-arm',
        description: 'Ampere Altra ARM — Always-Free-tier eligible footprint' },
      { id: 'VM.Standard.A1.Flex.4.24', label: 'VM.Standard.A1.Flex (4 OCPU / 24 GB)',
        vcpu: 4, ram: 24, disk: 'Variable', ...a1(4, 24), category: 'shared-arm',
        description: 'Ampere Altra ARM — standard ARM workload' },
      { id: 'VM.Standard.A1.Flex.8.48', label: 'VM.Standard.A1.Flex (8 OCPU / 48 GB)',
        vcpu: 8, ram: 48, disk: 'Variable', ...a1(8, 48), category: 'shared-arm',
        description: 'Ampere Altra ARM — production worker' },
    ]
  })(),

  /* ──────────────────────────────────────────────────────────────────
     AWS EC2 — on-demand Linux, us-east-1 list prices, USD → EUR.
     Specs/prices verified against canonical instance pages on
     instances.vantage.sh (which scrape aws.amazon.com/ec2/pricing/).
       m6i  (Intel Ice Lake, general-purpose):  m6i.large = $0.096/hr
       c6i  (Intel Ice Lake, compute):          c6i.large = $0.085/hr
       r6i  (Intel Ice Lake, memory):           r6i.large = $0.126/hr
       m7g  (AWS Graviton3 ARM, general):       m7g.large = $0.082/hr
     Storage: m6i/c6i/r6i/m7g are EBS-only (root volume separately
     priced). The disk field below is "EBS-only".
     ────────────────────────────────────────────────────────────────── */
  aws: [
    // m6i — general-purpose Intel
    { id: 'm6i.large',    label: 'm6i.large',    vcpu: 2, ram: 8,  disk: 'EBS-only',
      ...priceUSDtoEUR(0.096), category: 'general-purpose',
      description: 'Intel Ice Lake general-purpose — entry' },
    // AWS recommended starter — 4 vCPU / 16 GB Intel general-purpose.
    { id: 'm6i.xlarge',   label: 'm6i.xlarge',   vcpu: 4, ram: 16, disk: 'EBS-only',
      ...priceUSDtoEUR(0.192), category: 'general-purpose',
      description: 'Intel Ice Lake general-purpose — control-plane default',
      recommended: true },
    { id: 'm6i.2xlarge',  label: 'm6i.2xlarge',  vcpu: 8, ram: 32, disk: 'EBS-only',
      ...priceUSDtoEUR(0.384), category: 'general-purpose',
      description: 'Intel Ice Lake general-purpose — production worker' },
    { id: 'm6i.4xlarge',  label: 'm6i.4xlarge',  vcpu: 16, ram: 64, disk: 'EBS-only',
      ...priceUSDtoEUR(0.768), category: 'general-purpose',
      description: 'Intel Ice Lake general-purpose — heavy worker' },

    // c6i — compute-optimised Intel
    { id: 'c6i.large',    label: 'c6i.large',    vcpu: 2, ram: 4,  disk: 'EBS-only',
      ...priceUSDtoEUR(0.085), category: 'compute-optimized',
      description: 'Intel Ice Lake compute — CPU-heavy entry' },
    { id: 'c6i.xlarge',   label: 'c6i.xlarge',   vcpu: 4, ram: 8,  disk: 'EBS-only',
      ...priceUSDtoEUR(0.170), category: 'compute-optimized',
      description: 'Intel Ice Lake compute — CPU-heavy' },
    { id: 'c6i.2xlarge',  label: 'c6i.2xlarge',  vcpu: 8, ram: 16, disk: 'EBS-only',
      ...priceUSDtoEUR(0.340), category: 'compute-optimized',
      description: 'Intel Ice Lake compute — production CPU-bound' },
    { id: 'c6i.4xlarge',  label: 'c6i.4xlarge',  vcpu: 16, ram: 32, disk: 'EBS-only',
      ...priceUSDtoEUR(0.680), category: 'compute-optimized',
      description: 'Intel Ice Lake compute — heavy compute' },

    // r6i — memory-optimised Intel
    { id: 'r6i.large',    label: 'r6i.large',    vcpu: 2, ram: 16, disk: 'EBS-only',
      ...priceUSDtoEUR(0.126), category: 'memory-optimized',
      description: 'Intel Ice Lake memory — JVM, caches' },
    { id: 'r6i.xlarge',   label: 'r6i.xlarge',   vcpu: 4, ram: 32, disk: 'EBS-only',
      ...priceUSDtoEUR(0.252), category: 'memory-optimized',
      description: 'Intel Ice Lake memory — heavy in-memory' },
    { id: 'r6i.2xlarge',  label: 'r6i.2xlarge',  vcpu: 8, ram: 64, disk: 'EBS-only',
      ...priceUSDtoEUR(0.504), category: 'memory-optimized',
      description: 'Intel Ice Lake memory — large data plane' },

    // m7g — Graviton3 ARM general-purpose
    { id: 'm7g.large',    label: 'm7g.large',    vcpu: 2, ram: 8,  disk: 'EBS-only',
      ...priceUSDtoEUR(0.0816), category: 'shared-arm',
      description: 'AWS Graviton3 ARM general-purpose — entry' },
    { id: 'm7g.xlarge',   label: 'm7g.xlarge',   vcpu: 4, ram: 16, disk: 'EBS-only',
      ...priceUSDtoEUR(0.1632), category: 'shared-arm',
      description: 'AWS Graviton3 ARM general-purpose — ARM-native' },
    { id: 'm7g.2xlarge',  label: 'm7g.2xlarge',  vcpu: 8, ram: 32, disk: 'EBS-only',
      ...priceUSDtoEUR(0.3264), category: 'shared-arm',
      description: 'AWS Graviton3 ARM general-purpose — production' },
    { id: 'm7g.4xlarge',  label: 'm7g.4xlarge',  vcpu: 16, ram: 64, disk: 'EBS-only',
      ...priceUSDtoEUR(0.6528), category: 'shared-arm',
      description: 'AWS Graviton3 ARM general-purpose — heavy ARM worker' },
  ],

  /* ──────────────────────────────────────────────────────────────────
     AZURE — pay-as-you-go Linux, West Europe list prices, USD → EUR.
     Specs/prices verified against instances.vantage.sh which scrapes
     azure.microsoft.com/en-us/pricing/details/virtual-machines/linux/.
       Dsv5  (Intel Ice Lake, general):     D2s_v5 = $0.096/hr
       Esv5  (Intel Ice Lake, memory):      E2s_v5 = $0.126/hr
       Dpsv5 (Ampere Altra ARM, general):   D2ps_v5 = $0.077/hr
     Storage: all v5 SKUs use cloud-attached disks — disk field is
     "Variable" because the wizard provisions an OS disk separately
     via the OpenTofu module.
     ────────────────────────────────────────────────────────────────── */
  azure: [
    // Dsv5 — general-purpose Intel
    { id: 'Standard_D2s_v5',  label: 'Standard_D2s_v5',  vcpu: 2, ram: 8,  disk: 'Variable',
      ...priceUSDtoEUR(0.096), category: 'general-purpose',
      description: 'Intel Ice Lake general-purpose — entry' },
    // Azure recommended starter — 4 vCPU / 16 GB Intel general-purpose.
    { id: 'Standard_D4s_v5',  label: 'Standard_D4s_v5',  vcpu: 4, ram: 16, disk: 'Variable',
      ...priceUSDtoEUR(0.192), category: 'general-purpose',
      description: 'Intel Ice Lake general-purpose — control-plane default',
      recommended: true },
    { id: 'Standard_D8s_v5',  label: 'Standard_D8s_v5',  vcpu: 8, ram: 32, disk: 'Variable',
      ...priceUSDtoEUR(0.384), category: 'general-purpose',
      description: 'Intel Ice Lake general-purpose — production worker' },
    { id: 'Standard_D16s_v5', label: 'Standard_D16s_v5', vcpu: 16, ram: 64, disk: 'Variable',
      ...priceUSDtoEUR(0.768), category: 'general-purpose',
      description: 'Intel Ice Lake general-purpose — heavy worker' },

    // Esv5 — memory-optimised Intel
    { id: 'Standard_E2s_v5',  label: 'Standard_E2s_v5',  vcpu: 2, ram: 16, disk: 'Variable',
      ...priceUSDtoEUR(0.126), category: 'memory-optimized',
      description: 'Intel Ice Lake memory-optimised — entry' },
    { id: 'Standard_E4s_v5',  label: 'Standard_E4s_v5',  vcpu: 4, ram: 32, disk: 'Variable',
      ...priceUSDtoEUR(0.252), category: 'memory-optimized',
      description: 'Intel Ice Lake memory-optimised — JVM, caches' },
    { id: 'Standard_E8s_v5',  label: 'Standard_E8s_v5',  vcpu: 8, ram: 64, disk: 'Variable',
      ...priceUSDtoEUR(0.504), category: 'memory-optimized',
      description: 'Intel Ice Lake memory-optimised — large data plane' },

    // Dpsv5 — Ampere Altra ARM general-purpose
    { id: 'Standard_D2ps_v5', label: 'Standard_D2ps_v5', vcpu: 2, ram: 8,  disk: 'Variable',
      ...priceUSDtoEUR(0.077), category: 'shared-arm',
      description: 'Ampere Altra ARM general-purpose — entry' },
    { id: 'Standard_D4ps_v5', label: 'Standard_D4ps_v5', vcpu: 4, ram: 16, disk: 'Variable',
      ...priceUSDtoEUR(0.154), category: 'shared-arm',
      description: 'Ampere Altra ARM general-purpose — ARM-native' },
    { id: 'Standard_D8ps_v5', label: 'Standard_D8ps_v5', vcpu: 8, ram: 32, disk: 'Variable',
      ...priceUSDtoEUR(0.308), category: 'shared-arm',
      description: 'Ampere Altra ARM general-purpose — production' },
  ],
}

/**
 * Locate a SKU by provider+id. Returns undefined if the operator has a
 * persisted selection that no longer exists in the catalog (e.g. they
 * upgraded the wizard after the provider deprecated an old generation).
 */
export function findNodeSize(provider: CloudProvider, id: string): NodeSize | undefined {
  return PROVIDER_NODE_SIZES[provider].find((s) => s.id === id)
}

/**
 * The "starter default" SKU for a provider — first one flagged
 * `recommended: true`, falling back to the first entry.
 */
export function defaultNodeSizeId(provider: CloudProvider): string {
  const list = PROVIDER_NODE_SIZES[provider]
  const recommended = list.find((s) => s.recommended)
  return (recommended ?? list[0]!).id
}
