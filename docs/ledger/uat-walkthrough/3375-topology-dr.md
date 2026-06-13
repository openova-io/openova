# #3375 TOPOLOGY/DR — user acceptance walk (web UI)

**What the user does:** open a multi-region app, see its DR status, click **Switchover** to promote the other region, and confirm the app **keeps working with no data lost** — all from the console. **Precondition:** a real 2-region Sovereign with the app's multi-region option enabled.

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| `/app/<db-app>` → Topology | DR section shows **Phase = Healthy**, Primary region, Replica region, replication lag (green) | ☐ | |
| `/app/<db-app>` → Topology | ❌ **GAP** — the **Switchover** button is **disabled** ("Owner tier required"); an admin cannot run it in the live UI yet | ☐ | |
| `https://<app>.<sov>/` | Use the app's UI to create a record you'll recognise later (e.g. a Gitea repo) | ☐ | |
| `/app/<db-app>` → Topology | Click **Switchover…** → dialog lists the 7 steps + duration (<60s) → enter reason → **Confirm** | ☐ | |
| `/app/<db-app>` → Topology | Watch the panel advance (validate → drain → flip DNS → promote → audit) → **other region is now primary**, last switchover **Success** | ☐ | |
| `https://<app>.<sov>/` | Re-open the app → it loads and works (served from the promoted region) | ☐ | |
| `https://<app>.<sov>/` | The record you created earlier is **still there** (zero data loss) | ☐ | |
| `/app/<db-app>` → Topology | After the original region returns → panel shows **one** primary (the promoted region), old region a follower (no split-brain) | ☐ | |
| `https://gitea.<sov>/` | After a gitea switchover: clone/push works again; a previously-pushed file is present | ☐ | |
| `https://bao.<sov>/ui/` | During an openbao switchover: reading a stored secret in the Vault UI stays available throughout | ☐ | |
| `/app/openbao` → Topology | ❌ **GAP** — the openbao *promotion* may not be wired to the console switchover; if it can't be driven from the UI, that half isn't walkable | ☐ | |

**Verdict:** the DR console UI exists, but two things block a clean walk today: the **Switchover button is disabled** (tier bug), and the cross-region machinery is **off unless explicitly enabled** on a genuine 2-region Sovereign.
