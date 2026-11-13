# hw307 walk of the first north-star screen changes, 13 November 2026

Environment: hw307 (`console.hw307.omani.works`, `marketplace.hw307.omani.works`), region A control plane.
Release on the environment: umbrella chart `1.4.1906-hw307.23-ns.1`, images `catalyst-ui:hw307-2e1d885` and `marketplace:hw307-2e1d885`.
Source: branch `hw307/north-star`, which is the line hw307 already ran (`1.4.1906-hw307.23`) plus the screen parts of seven merged tasks, applied without conflict.
Walked in a browser as the signed-in owner (console) and as a signed-out visitor (storefront), between 05:16 and 05:40 UTC.

hw307 does not run the main branch. It runs a separate line of the disaster-recovery work that is not merged to main yet. Only files under `products/catalyst/bootstrap/ui` and `core/marketplace` were changed on top of it. The API of hw307 was not changed.

## What was seen

| Task | Seen on hw307 | Screenshot |
|---|---|---|
| SVC-030 | Before: the Catalog showed 92 cards and no switch. After: 29 cards (the listed Blueprints) and a "Show unlisted" switch; with the switch on, 92 cards and the address ends in `?unlisted=1`. Not seen: the "Not installable: no published chart" state, because no catalog entry of hw307 is at version 0.0.0. | `hw307-2026-11-13-before-catalog.png`, `...-after-catalog-listed.png`, `...-after-catalog-unlisted.png` |
| NET-041 | Storefront add-ons step lists API Access, Daily Backup, Dedicated IP and Log Management; no Web Application Firewall. The text "Web Application Firewall" was in the served add-ons script before and is in none of the served scripts after. The wizard step "Platform Components" shows Cilium and no Coraza or WAF entry. | `...-after-storefront-addons.png`, `...-after-wizard-components.png` |
| BSS-035 | Plan step with prices readable: packages S and M with prices. With the price request answered 500 in the browser: "Prices are unavailable. Please try again shortly." and the Continue button disabled. | `...-after-storefront-plans-normal.png`, `...-after-storefront-prices-unavailable.png` |
| NET-070 | Wizard step 2 reads "Choose the number of regions" with two choices: "One region" and "Two or more regions" (2 regions). | `...-after-wizard-topology.png` |
| NET-072 | "New instance" of wordpress-tenant: the placement selector offers "Default (server picks)" and "host — host cluster (no vCluster)" only. | `...-after-new-instance-placement.png` |
| SOV-220 | Settings, Sovereignty: the card lists 11 steps, all completed, 100%, while the status response names 14 entries (three are per-region copies). Observation: the card's heading still reads "Cutover in progress" at 100%. | `...-after-sovereignty-cutover.png` |

## Not delivered in this release

- SOV-030: its enforcing half is in the API, which was not changed on hw307. The console now sends the confirmation header, but the API of hw307 does not demand it yet.
- FND-094 and FND-097: they remove unused pages and do not apply to the line hw307 runs.

## State of hw307 after the release

The upgrade of `bp-catalyst-platform` succeeded in about one minute. The HelmReleases that are not Ready are the same six as before the release (`acmewalk307/bp-agenity`, `acmewalk307/bp-wordpress-tenant` and four that are not installed on this cloud). The console and the storefront answer 200.
