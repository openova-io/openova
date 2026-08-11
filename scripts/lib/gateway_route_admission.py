"""Gateway API route <-> listener admission — the ONE implementation.

WHY THIS MODULE EXISTS
----------------------
Two guards need to answer the same question, "does any listener of this
route's parent Gateway admit this hostname?", from two different inputs:

  * scripts/check-live-route-backends.sh   — from a running cluster (#6165)
  * scripts/check-chart-route-listener-correspondence.sh
                                           — from `helm template` output,
                                             no cluster at all (#6140)

The rule they share is small, subtle, and wrong in interesting ways if
re-typed: Gateway API wildcards are SUFFIX matches, so `*.a.b` admits
`x.a.b` AND `x.y.a.b` but never the bare apex `a.b`. A second copy of that
rule in a second file is a copy that will drift, and the drift would be
invisible — both guards would keep passing, each against its own idea of
the spec. So the rule lives here once and both import it.

Nothing in this module touches a cluster, a network, or the filesystem.

Reference: gateway-api Listener.hostname / HTTPRoute.hostnames.
"""


def listener_admits(listener_hostname, route_hostname):
    """Does listener hostname `listener_hostname` admit `route_hostname`?

    Spec, and every clause is load-bearing:

      * a listener with NO hostname admits every host;
      * a route with NO hostname inherits the listener's, so it is admitted;
      * `*.a.b` is a SUFFIX match one or more labels deep — it admits
        `x.a.b` and `x.y.a.b`, and NEVER the bare `a.b` it wildcards;
      * a concrete listener cannot admit a wildcard route.

    The apex clause is the whole bug class. A naive `endswith("a.b")` gets
    it wrong in BOTH directions at once: it would wrongly admit the apex
    `a.b`, and it would wrongly admit `nota.b`. A "exactly one label" rule
    is the opposite error — it would reject the legitimate deep host
    `x.y.a.b` that Gateway API requires a wildcard to accept.
    """
    if not listener_hostname:
        return True
    if not route_hostname:
        return True
    if listener_hostname.startswith("*."):
        suffix = listener_hostname[1:]          # "*.a.b" -> ".a.b"
        if route_hostname.startswith("*."):
            return route_hostname[1:] == suffix or route_hostname[1:].endswith(suffix)
        return route_hostname.endswith(suffix)
    if route_hostname.startswith("*."):
        return False
    return listener_hostname == route_hostname


def correspondence_rows(routes, gateways):
    """Classify every advertised hostname against its parent Gateways.

    `routes`   [{"ns","name","hosts":[..],
                 "parents":[{"ns","name","sectionName","port"}]}]
    `gateways` [{"ns","name","listeners":[{"name","hostname","port"}]}]

    Returns (rows, all_hosts) where rows are
    (verdict, hostname, "ns/name", why) and verdict is one of:

      SERVING     — some parent listener admits the hostname.
      NO-LISTENER — the parent Gateway EXISTS but no listener of it admits
                    the hostname. Owner: whoever declares the listener set
                    (for the console gateway that is
                    `consoleGateway.hostPrefixes`; for the shared gateway,
                    `parentZones`). Gateway API reports
                    `Accepted=False NoMatchingListenerHostname`, and envoy
                    resets the TLS handshake before any HTTP status exists.
      NO-GATEWAY  — the parentRef names a Gateway that is not present at
                    all. Owner: whoever installs the Gateway (a bootstrap
                    slot / a region's Kustomization), NOT the listener set.

    Keeping those two apart matters because they route to different people:
    NO-LISTENER is a one-line values change, NO-GATEWAY is a missing
    install. Collapsing them into "route is broken" sends every one of them
    to the wrong owner.

    `sectionName` and `port` on a parentRef are PINS: a route that names a
    section only attaches to the listener of that exact name, and a route
    that pins a port only attaches to listeners on that port. A pin that
    matches nothing leaves the route with zero candidate listeners, which
    is NO-LISTENER — not a pass.
    """
    gws = {(g["ns"], g["name"]): g for g in gateways}
    rows, all_hosts = [], set()

    for r in routes:
        parents = r.get("parents") or []
        for h in (r.get("hosts") or []):
            all_hosts.add(h)
        if not parents:
            continue
        for h in (r.get("hosts") or [""]):
            admitted, absent_gws, seen_listeners = False, [], 0
            for p in parents:
                key = (p.get("ns") or r["ns"], p["name"])
                g = gws.get(key)
                if g is None:
                    absent_gws.append("%s/%s" % key)
                    continue
                for ls in (g.get("listeners") or []):
                    if p.get("sectionName") and ls.get("name") != p["sectionName"]:
                        continue
                    if p.get("port") and int(ls.get("port") or 0) != int(p["port"]):
                        continue
                    seen_listeners += 1
                    if listener_admits(ls.get("hostname"), h):
                        admitted = True
                        break
                if admitted:
                    break
            who = "%s/%s" % (r["ns"], r["name"])
            shown = h or "(inherits listener)"
            if admitted:
                rows.append(("SERVING", shown, who, "admitted by a parent listener"))
            elif absent_gws and seen_listeners == 0:
                rows.append(("NO-GATEWAY", shown, who,
                             "parent Gateway absent here: "
                             + ",".join(sorted(set(absent_gws)))))
            else:
                rows.append(("NO-LISTENER", shown, who,
                             "no parent listener admits it "
                             "(%d listener(s) considered)" % seen_listeners))
    return rows, all_hosts
