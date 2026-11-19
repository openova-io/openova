#!/usr/bin/env python3
"""gate-outside-dial.py - what the API port of a Sovereign answers from outside.

  python3 scripts/gate-outside-dial.py addresses <nodes.json> [<nodes.json> ...]
  python3 scripts/gate-outside-dial.py dial <address>,<address>,...
  python3 scripts/gate-outside-dial.py dial-peer <address>,<address>,...
  python3 scripts/gate-outside-dial.py --self-test
The workflow .github/workflows/foundation-gate.yaml runs it for gate checks NET-G02 and NET-G01.

addresses  runs on the gate runner. Each file is the output of `kubectl get nodes -o json` of one
           region. It prints, on one line and joined by commas, the ExternalIP addresses of the
           nodes with the label catalyst.openova.io/role=control-plane, sorted, each once. A
           file without such an address, and an address that is not an IP address, is an error.
dial       runs on a runner hosted by GitHub, whose address is in no list of the firewall. It
           opens a TCP connection to port 6443 of each address and prints
           <address>=<outcome>,<address>=<outcome> on one line: the value of the suite's flag
           --api-port-outside. An outcome is open (accepted), refused, timeout (no answer in 5
           seconds) or error (anything else). An address is dialled up to three times; it is
           timeout only when all three timed out. The tool does not judge: check NET-G02 does.
dial-peer  the same, on the same runner, for ports 2379 (the embedded etcd) and 12379 (the mesh
           endpoint) of each address. It prints <address>:<port>=<outcome>,... on one line, an
           IPv6 address in brackets: the value of the suite's flag --peer-ports-outside, which
           check NET-G01 judges. A dial from the machine that runs the suite is no outside dial:
           the firewall lists that machine (gate run 44455908137 saw refused from the parent).
Exit 0: the line was printed. 2: an input cannot be read or the usage is wrong.
"""
import ipaddress, json, socket, sys

PORT, TIMEOUT, ATTEMPTS = 6443, 5.0, 3
PEER_PORTS = (2379, 12379)
ROLE_LABEL = "catalyst.openova.io/role"


class Bad(Exception):
    """An input cannot be read: exit 2."""


def ip(text):
    try:
        return str(ipaddress.ip_address(text.strip()))
    except ValueError:
        raise Bad("%r is not an IP address" % text)


def addresses(lists):
    """lists: pairs of a name and the parsed output of `kubectl get nodes -o json`."""
    out = set()
    for name, nodes in lists:
        found = set()
        for node in nodes.get("items") or []:
            if ((node.get("metadata") or {}).get("labels") or {}).get(ROLE_LABEL) != "control-plane":
                continue
            for a in (node.get("status") or {}).get("addresses") or []:
                if a.get("type") == "ExternalIP" and a.get("address"):
                    found.add(ip(a["address"]))
        if not found:
            raise Bad("%s: no control-plane node has an ExternalIP address" % name)
        out |= found
    return sorted(out)


def connect(address, port=None):
    """One TCP connection to port (PORT when None) of address. Returns None, or the error."""
    try:
        socket.create_connection((address, PORT if port is None else port), timeout=TIMEOUT).close()
    except OSError as e:
        return e
    return None


def outcome(error):
    if error is None:
        return "open"
    if isinstance(error, (socket.timeout, TimeoutError)):
        return "timeout"
    if isinstance(error, ConnectionRefusedError):
        return "refused"
    return "error"


def hostport(address, port):
    return "[%s]:%d" % (address, port) if ":" in address else "%s:%d" % (address, port)


def dial(text, connect=connect, ports=None):
    """text: addresses joined by commas. Returns the line for --api-port-outside, or, with ports,
    the line for --peer-ports-outside."""
    targets = [ip(a) for a in text.split(",") if a.strip()]
    if not targets or len(set(targets)) != len(targets):
        raise Bad("give every address once, joined by commas; got %r" % text)
    out = []
    for address in targets:
        for port in ports or (None,):
            got = "timeout"
            for _ in range(ATTEMPTS):
                got = outcome(connect(address) if port is None else connect(address, port))
                if got != "timeout":
                    break
            out.append("%s=%s" % (address if port is None else hostport(address, port), got))
    return ",".join(out)


def self_test():
    results = []

    def check(name, good, detail=""):
        results.append((name, bool(good), detail))

    def bad(fn, *args):
        try:
            return fn(*args)
        except Bad as e:
            return "Bad: %s" % e

    def node(role, *pairs):
        return {"metadata": {"labels": {ROLE_LABEL: role} if role else {}},
                "status": {"addresses": [{"type": t, "address": a} for t, a in pairs]}}
    a = {"items": [node("control-plane", ("InternalIP", "10.20.0.5"), ("ExternalIP", "203.0.113.9")),
                   node("", ("ExternalIP", "198.51.100.9"))]}
    b = {"items": [node("control-plane", ("ExternalIP", "203.0.113.2")), node("control-plane", ("ExternalIP", "203.0.113.2")),
                   node("control-plane", ("ExternalIP", "203.0.113.10"))]}
    got = bad(addresses, [("a", a), ("b", b)])
    check("addresses: control-plane ExternalIP only, each once, sorted", got == ["203.0.113.10", "203.0.113.2", "203.0.113.9"], got)
    got = bad(addresses, [("a", a), ("b", {"items": [node("control-plane", ("InternalIP", "10.30.0.5")), node("", ("ExternalIP", "198.51.100.8"))]})])
    check("addresses: a region without an address is refused", got == "Bad: b: no control-plane node has an ExternalIP address", got)
    got = bad(addresses, [("a", {"items": [node("control-plane", ("ExternalIP", "cp.example.org"))]})])
    check("addresses: a name is refused", str(got).startswith("Bad: 'cp.example.org' is not an IP address"), got)
    for error, want in ((None, "open"), (socket.timeout("timed out"), "timeout"), (TimeoutError(110, "Connection timed out"), "timeout"),
                        (ConnectionRefusedError(111, "Connection refused"), "refused"), (OSError(101, "Network is unreachable"), "error"),
                        (ConnectionResetError(104, "Connection reset by peer"), "error")):
        check("outcome: %r is %s" % (error, want), outcome(error) == want, outcome(error))
    calls = []

    def fake(answers):
        def f(address):
            calls.append(address)
            left = answers[address]
            return left.pop(0) if len(left) > 1 else left[0]
        return f
    t, r = socket.timeout("timed out"), ConnectionRefusedError(111, "Connection refused")
    got = bad(dial, " 203.0.113.9,203.0.113.2 ,2001:DB8::1,192.0.2.7", fake({"203.0.113.9": [t], "203.0.113.2": [t, None], "2001:db8::1": [r], "192.0.2.7": [t, t, OSError(113, "No route to host")]}))
    check("dial: the line, in the order given", got == "203.0.113.9=timeout,203.0.113.2=open,2001:db8::1=refused,192.0.2.7=error", got)
    check("dial: three attempts on a time-out, and no more after an answer", calls == ["203.0.113.9"] * 3 + ["203.0.113.2"] * 2 + ["2001:db8::1"] + ["192.0.2.7"] * 3, calls)
    for text in ("", " , ", "203.0.113.9,203.0.113.9", "203.0.113.9,cp.example.org", "203.0.113.9:6443", "203.0.113.9 203.0.113.2"):
        got = bad(dial, text, fake({}))
        check("dial: %r is refused" % text, str(got).startswith("Bad: "), got)
        got = bad(dial, text, fake({}), PEER_PORTS)
        check("dial-peer: %r is refused" % text, str(got).startswith("Bad: "), got)
    peer_calls = []

    def fake_peer(answers):
        def f(address, port=None):
            peer_calls.append(hostport(address, port))
            left = answers[hostport(address, port)]
            return left.pop(0) if len(left) > 1 else left[0]
        return f
    got = bad(dial, " 203.0.113.9,2001:DB8::1 ", fake_peer({"203.0.113.9:2379": [t], "203.0.113.9:12379": [t, r],
                                                          "[2001:db8::1]:2379": [None], "[2001:db8::1]:12379": [OSError(113, "No route to host")]}), PEER_PORTS)
    check("dial-peer: the line, ports 2379 and 12379 of each address in the order given",
          got == "203.0.113.9:2379=timeout,203.0.113.9:12379=refused,[2001:db8::1]:2379=open,[2001:db8::1]:12379=error", got)
    check("dial-peer: three attempts on a time-out, and no more after an answer",
          peer_calls == ["203.0.113.9:2379"] * 3 + ["203.0.113.9:12379"] * 2 + ["[2001:db8::1]:2379", "[2001:db8::1]:12379"], peer_calls)
    check("dial-peer: never port 6443", not any(c.endswith(":6443") for c in peer_calls), peer_calls)
    # The real connection, on this machine only: a listening port and a closed one.
    global PORT
    saved, listener = PORT, socket.socket()
    try:
        listener.bind(("127.0.0.1", 0))
        listener.listen(16)  # every dial below stays in the backlog: nothing accepts
        PORT = listener.getsockname()[1]
        check("connect: a listening port is open", dial("127.0.0.1") == "127.0.0.1=open", dial("127.0.0.1"))
        want = "127.0.0.1:%d=open" % PORT
        check("connect: a listening peer port is open", dial("127.0.0.1", ports=(PORT,)) == want, dial("127.0.0.1", ports=(PORT,)))
        listener.close()
        check("connect: a closed port is refused", dial("127.0.0.1") == "127.0.0.1=refused", dial("127.0.0.1"))
        want = "127.0.0.1:%d=refused" % PORT
        check("connect: a closed peer port is refused", dial("127.0.0.1", ports=(PORT,)) == want, dial("127.0.0.1", ports=(PORT,)))
    finally:
        listener.close()
        PORT = saved
    wrong = [(n, d) for n, good, d in results if not good]
    for name, detail in wrong:
        print("self-test: WRONG: %s %r" % (name, detail))
    print("self-test: FAILED, %d of %d cases wrong" % (len(wrong), len(results)) if wrong else "self-test: %d cases passed" % len(results))
    return 1 if wrong else 0


def main(argv):
    try:
        if argv == ["--self-test"]:
            return self_test()
        if len(argv) >= 2 and argv[0] == "addresses":
            lists = []
            for path in argv[1:]:
                try:
                    with open(path) as f:
                        lists.append((path, json.load(f)))
                except (OSError, ValueError) as e:
                    raise Bad("%s cannot be read: %s" % (path, e))
            print(",".join(addresses(lists)))
            return 0
        if len(argv) == 2 and argv[0] == "dial":
            print(dial(argv[1]))
            return 0
        if len(argv) == 2 and argv[0] == "dial-peer":
            print(dial(argv[1], ports=PEER_PORTS))
            return 0
    except Bad as e:
        print("gate-outside-dial: %s" % e, file=sys.stderr)
        return 2
    print("usage: python3 scripts/gate-outside-dial.py addresses <nodes.json>... | dial <address>,<address>,... | dial-peer <address>,<address>,... | --self-test", file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
