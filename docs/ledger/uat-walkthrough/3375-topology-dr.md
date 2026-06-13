# #3375 TOPOLOGY/DR — user acceptance walk (100% web UI)

**What the user does:** open a multi-region app, see its disaster-recovery status, click **Switchover** to promote the other region, and confirm the app **keeps working with no data lost** — all from the console. **Precondition:** the Sovereign is a real 2-region deployment with the app's multi-region option enabled (a single-region app has no DR to test).

_Each line = one browser step. `☐`/`✅`/`❌`. No terminal._

### See the DR status of a multi-region app
- [ ] Sign in at `https://console.<sov>/` → open the multi-region database app's detail page → click the **Topology** tab.
- [ ] The DR section shows: **Phase = Healthy**, **Primary region**, **Replica region**, and a **replication lag** indicator (green / small).
- [ ] ❌ **GAP** — the **Switchover** button is present but **disabled** ("Owner tier required"), because the page doesn't pass the caller's tier through. Today an admin cannot actually click it in the live UI — this must be fixed before the walk can proceed via the button.

### Create some data first (so "no data loss" is provable in the UI)
- [ ] Use the app through its own web UI to create a record you'll recognise later — e.g. open the **Gitea** app and create a repo, or add a row via the app — and remember it.

### Trigger the switchover from the console
- [ ] App detail → **Topology** tab → click **Switchover…** → a dialog lists the 7 steps and an estimated duration (<60s, <5s write disruption) → enter a reason → **Confirm Switchover**.
- [ ] Watch the status panel advance through the steps (validate → drain → flip DNS → promote → audit) until it shows the **other region is now primary** and **last switchover = Success**.

### Confirm the app still works and the data survived
- [ ] Re-open the app's own URL in the browser → it loads and works (now served from the promoted region).
- [ ] Find the record you created earlier → it is **still there** (zero data loss), via the app's web UI.

### Rejoin (no split-brain)
- [ ] Back on the **Topology** tab, after the original region returns → the panel shows exactly **one** primary (the promoted region) and the old region as a follower — not two primaries.

### Other agreed apps (same shape, via each app's UI)
- [ ] **Gitea**: after a switchover, clone/push works again from the browser-shown repo within the stated time, and a previously-pushed file is present.
- [ ] **OpenBao**: during the switchover, reading a stored secret in the Vault UI stays available the whole time.
- [ ] ❌ **GAP** — the OpenBao *promotion* step may not be wired to the console switchover; if the DR panel can't drive it, that half isn't user-walkable yet.

**Verdict:** the DR **console UI exists** (status + switchover dialog + step panel), but two things block a clean web-UI walk today: the **Switchover button is disabled** (tier not threaded), and the cross-region machinery is **off unless explicitly enabled** on a genuine 2-region Sovereign. Both must be true before this walks.
