# #3375 TOPOLOGY/DR — user acceptance walk (web UI)

**What the user does:** open a multi-region app, see its DR status, click **Switchover** to promote the other region, confirm the app **keeps working with no data lost**. **Precondition:** a real 2-region Sovereign with the app's multi-region option enabled.

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [bp-cnpg-pair · Topology](https://console.hw133.omani.works/app/bp-cnpg-pair) | DR section shows **Phase = Healthy**, Primary region, Replica region, replication lag (green) | ☐ | |
| [bp-cnpg-pair · Topology](https://console.hw133.omani.works/app/bp-cnpg-pair) | ❌ **GAP** — the **Switchover** button is **disabled** ("Owner tier required"); an admin can't run it in the live UI yet | ☐ | |
| [gitea.hw133.omani.works](https://gitea.hw133.omani.works/) | Use the app to create a record you'll recognise later (e.g. a Gitea repo) | ☐ | |
| [bp-cnpg-pair · Topology](https://console.hw133.omani.works/app/bp-cnpg-pair) | Click **Switchover…** → dialog lists 7 steps + duration (<60s) → enter reason → **Confirm** | ☐ | |
| [bp-cnpg-pair · Topology](https://console.hw133.omani.works/app/bp-cnpg-pair) | Watch the panel advance → **other region now primary**, last switchover **Success** | ☐ | |
| [gitea.hw133.omani.works](https://gitea.hw133.omani.works/) | Re-open the app → it loads and works (served from the promoted region) | ☐ | |
| [gitea.hw133.omani.works](https://gitea.hw133.omani.works/) | The record you created earlier is **still there** (zero data loss) | ☐ | |
| [bp-cnpg-pair · Topology](https://console.hw133.omani.works/app/bp-cnpg-pair) | After the original region returns → **one** primary (the promoted region), old region a follower (no split-brain) | ☐ | |
| [bao.hw133.omani.works/ui/](https://bao.hw133.omani.works/ui/) | During an openbao switchover: reading a stored secret in the Vault UI stays available throughout | ☐ | |
| [bp-openbao · Topology](https://console.hw133.omani.works/app/bp-openbao) | ❌ **GAP** — the openbao *promotion* may not be wired to the console switchover | ☐ | |

**Verdict:** the DR console UI exists, but two things block a clean walk today: the **Switchover button is disabled** (tier bug), and the cross-region machinery is **off unless explicitly enabled** on a genuine 2-region Sovereign.
