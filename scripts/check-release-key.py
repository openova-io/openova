#!/usr/bin/env python3
"""check-release-key.py - the public half of the release key changes only in a marked rotation.

keys/release-cosign.pub is the public half of the release signing key. On main the shared
signing action .github/actions/sign-and-attest/action.yaml signs every platform image and
chart with the private half (the repository secret RELEASE_COSIGN_KEY) and at once verifies
that signature against keys/release-cosign.pub of the commit its run checked out. So the
file may change only in a rotation, by the steps of docs/RUNBOOKS.md section 2.16, and
docs/SECURITY.md section 11.5 records the SHA-256 of the key in use.

Usage, from the repository root:

  python3 scripts/check-release-key.py tree
  python3 scripts/check-release-key.py pr --base <commit> --head <commit> --body-file <file>
  python3 scripts/check-release-key.py --self-test

tree  The rules of the checkout.
  T1  keys/release-cosign.pub is one PEM public key: the line "-----BEGIN PUBLIC KEY-----",
      base64 lines, the line "-----END PUBLIC KEY-----" and a final newline. The decoded key
      is 91 bytes long and is an ECDSA P-256 key, the kind "cosign generate-key-pair" makes.
  T2  docs/SECURITY.md has exactly one line that starts with "Release key in use: sha256:",
      and the 64 hex characters after it are the SHA-256 of keys/release-cosign.pub.

pr    The rules of a pull request. The change is the difference between the merge base of
      <base> and <head>, and <head>. A rotation is marked by exactly one line of the
      pull-request body of the form "Release-Key-Rotation: sha256:<64 hex>".
  P1  keys/release-cosign.pub is not deleted.
  P2  a pull request that changes keys/release-cosign.pub has a marker line.
  P3  the body has at most one marker line, and it has the form above.
  P4  the hex of the marker line is the SHA-256 of keys/release-cosign.pub at <head>.
  P5  the new keys/release-cosign.pub is none that the file held at an earlier commit of
      the merge base: a replaced public key is never trusted again.
  P6  a pull request that does not change keys/release-cosign.pub has no marker line.

Exit codes: 0 every rule holds; 1 a rule is broken, one line per finding; 2 wrong usage or
a commit that cannot be read. Only the Python standard library and git are used.
"""
import base64
import hashlib
import os
import re
import subprocess
import sys
import tempfile

KEY = "keys/release-cosign.pub"
DOC = "docs/SECURITY.md"
BEGIN, END = "-----BEGIN PUBLIC KEY-----", "-----END PUBLIC KEY-----"
P256 = bytes.fromhex("3059301306072a8648ce3d020106082a8648ce3d030107034200")
RECORD = re.compile(r"^Release key in use: sha256:([0-9a-f]{64})(?![0-9a-zA-Z])")
MARKER = re.compile(r"^Release-Key-Rotation:[ \t]*(\S*)[ \t\r]*$", re.M)
MARK_VALUE = re.compile(r"^sha256:([0-9a-f]{64})$")
NAME = "check-release-key"


class Unreadable(Exception):
    pass


def git(root, *args):
    r = subprocess.run(["git", "-C", root] + list(args), stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if r.returncode != 0:
        raise Unreadable("git %s: %s" % (" ".join(args), r.stderr.decode("utf-8", "replace").strip()))
    return r.stdout


def sha(data):
    return hashlib.sha256(data).hexdigest()


def key_findings(data):
    """T1 on the bytes of keys/release-cosign.pub."""
    bad = ["T1: %s is not one PEM public key of ECDSA P-256" % KEY]
    try:
        lines = data.decode("ascii").split("\n")
    except UnicodeDecodeError:
        return bad
    if len(lines) < 4 or lines[0] != BEGIN or lines[-2] != END or lines[-1] != "":
        return bad
    try:
        der = base64.b64decode("".join(lines[1:-2]), validate=True)
    except ValueError:
        return bad
    if len(der) != 91 or not der.startswith(P256):
        return bad
    return []


def tree(root):
    out = []
    try:
        key = open(os.path.join(root, KEY), "rb").read()
    except OSError:
        return ["T1: %s cannot be read" % KEY]
    out += key_findings(key)
    try:
        doc = open(os.path.join(root, DOC), encoding="utf-8").read().split("\n")
    except OSError:
        return out + ["T2: %s cannot be read" % DOC]
    found = [m.group(1) for m in (RECORD.match(x) for x in doc) if m]
    if len(found) != 1:
        out.append("T2: %s must have exactly one line 'Release key in use: sha256:<64 hex>'; it has %d" % (DOC, len(found)))
    elif found[0] != sha(key):
        out.append("T2: %s records the key %s, but %s has the SHA-256 %s" % (DOC, found[0], KEY, sha(key)))
    return out


def blob(root, commit, path):
    """The bytes of path at commit, or None when the file is absent there."""
    if not git(root, "ls-tree", "--name-only", commit, "--", path).strip():
        return None
    return git(root, "show", "%s:%s" % (commit, path))


def pr(root, base, head, body):
    mb = git(root, "merge-base", base, head).decode().strip()
    status = git(root, "diff", "--no-renames", "--name-status", mb, head, "--", KEY).decode().split()
    marks = MARKER.findall(body)
    out = []
    if len(marks) > 1:
        out.append("P3: the body has %d lines 'Release-Key-Rotation:'; a rotation has exactly one" % len(marks))
    elif marks and not MARK_VALUE.match(marks[0]):
        out.append("P3: the line 'Release-Key-Rotation: %s' is not of the form 'Release-Key-Rotation: sha256:<64 hex>'" % marks[0])
    if not status:
        if marks:
            out.append("P6: the body marks a rotation, but this pull request does not change %s" % KEY)
        return out
    if status[0] == "D":
        return out + ["P1: this pull request deletes %s; a rotation replaces the key and never deletes it" % KEY]
    new = blob(root, head, KEY)
    if not marks:
        out.append("P2: this pull request changes %s and is not marked as a rotation: its body has no line "
                   "'Release-Key-Rotation: sha256:<SHA-256 of the new file>' (docs/RUNBOOKS.md section 2.16)" % KEY)
    elif len(marks) == 1 and MARK_VALUE.match(marks[0]) and MARK_VALUE.match(marks[0]).group(1) != sha(new):
        out.append("P4: the body names the key %s, but %s at the head has the SHA-256 %s" % (marks[0], KEY, sha(new)))
    for c in git(root, "log", "--format=%H", mb, "--", KEY).decode().split():
        old = blob(root, c, KEY)
        if old is not None and sha(old) == sha(new):
            out.append("P5: %s at the head is the key it held at commit %s; a replaced key is never trusted again" % (KEY, c))
            break
    return out


# ---- self-test
class T:
    def __init__(self):
        self.n, self.failed = 0, []

    def ok(self, cond, what):
        self.n += 1
        if not cond:
            self.failed.append(what)


def fake_key(n):
    der = P256 + bytes([4]) + bytes((n * 7 + i) % 256 for i in range(64))
    b = base64.b64encode(der).decode()
    return ("%s\n%s\n%s\n%s\n" % (BEGIN, b[:64], b[64:], END)).encode()


def doc_for(key):
    return "# Security\n\nRelease key in use: sha256:%s\n\nText.\n" % sha(key)


def self_test():
    t = T()
    k1, k2, k3 = fake_key(1), fake_key(2), fake_key(3)
    with tempfile.TemporaryDirectory(prefix="release-key-selftest-") as root:
        git(root, "init", "-q", "-b", "main")
        git(root, "config", "user.email", "t@example.invalid")
        git(root, "config", "user.name", "t")
        os.makedirs(os.path.join(root, "keys"))
        os.makedirs(os.path.join(root, "docs"))

        def write(path, data):
            with open(os.path.join(root, path), "wb") as f:
                f.write(data if isinstance(data, bytes) else data.encode())

        def commit(msg):
            git(root, "add", "-A")
            git(root, "commit", "-q", "--allow-empty", "-m", msg)
            return git(root, "rev-parse", "HEAD").decode().strip()

        # History of main: k1, then a rotation to k2. Base is the commit with k2.
        write(KEY, k1)
        write(DOC, doc_for(k1))
        commit("k1")
        write(KEY, k2)
        write(DOC, doc_for(k2))
        base = commit("k2")

        # tree
        t.ok(tree(root) == [], "tree: a P-256 key recorded in the document passes")
        for bad, what in ((k2.replace(BEGIN.encode(), b"-----BEGIN KEY-----"), "a wrong first line"),
                          (k2[:-1], "no final newline"),
                          (k2.replace(b"\n" + END.encode(), b"\n!!!!\n" + END.encode()), "a line that is not base64"),
                          (("%s\n%s\n%s\n" % (BEGIN, base64.b64encode(b"\x30" * 91).decode(), END)).encode(), "a key that is not P-256"),
                          (("%s\n%s\n%s\n" % (BEGIN, base64.b64encode(P256 + b"\x04" * 66).decode(), END)).encode(), "a key of the wrong length")):
            write(KEY, bad)
            write(DOC, doc_for(bad))
            t.ok([x[:3] for x in tree(root)] == ["T1:"], "tree T1: %s fails" % what)
        write(KEY, k2)
        write(DOC, doc_for(k1))
        t.ok([x[:3] for x in tree(root)] == ["T2:"], "tree T2: a document that records another key fails")
        write(DOC, "# Security\n\nNo record.\n")
        t.ok([x[:3] for x in tree(root)] == ["T2:"], "tree T2: a document without the record fails")
        write(DOC, doc_for(k2) + doc_for(k2))
        t.ok([x[:3] for x in tree(root)] == ["T2:"], "tree T2: a document with two records fails")
        write(DOC, doc_for(k2).replace("sha256:", "sha256: "))
        t.ok([x[:3] for x in tree(root)] == ["T2:"], "tree T2: a record that is not of the form fails")
        os.remove(os.path.join(root, KEY))
        t.ok([x[:3] for x in tree(root)] == ["T1:"], "tree T1: a missing key file fails")
        git(root, "checkout", "-q", "--", KEY, DOC)
        t.ok(tree(root) == [], "tree: the restored checkout passes")

        def branch(name, files, delete=()):
            git(root, "checkout", "-q", "-b", name, base)
            for p, d in files:
                write(p, d)
            for p in delete:
                os.remove(os.path.join(root, p))
            head = commit(name)
            git(root, "checkout", "-q", "main")
            return head

        mark = lambda k: "Text.\n\nRelease-Key-Rotation: sha256:%s\n\nRefs #1\n" % sha(k)
        plain = branch("plain", [("docs/other.md", "x\n")])
        rotate = branch("rotate", [(KEY, k3), (DOC, doc_for(k3))])
        back = branch("back", [(KEY, k1), (DOC, doc_for(k1))])
        gone = branch("gone", [], delete=[KEY])

        def codes(head, body):
            return [x[:3] for x in pr(root, base, head, body)]

        t.ok(codes(plain, "Refs #1\n") == [], "pr: a pull request that leaves the key alone passes")
        t.ok(codes(rotate, mark(k3)) == [], "pr: a marked rotation passes")
        t.ok(codes(rotate, mark(k3).replace("\n", "\r\n")) == [], "pr: a marked rotation with Windows line ends passes")
        t.ok(codes(rotate, "Refs #1\n") == ["P2:"], "pr P2: a change of the key that is not marked fails")
        t.ok(codes(rotate, mark(k3) + mark(k3)) == ["P3:"], "pr P3: two marker lines fail")
        t.ok(codes(rotate, "Release-Key-Rotation: yes\n") == ["P3:"], "pr P3: a marker line of another form fails")
        t.ok(codes(rotate, mark(k2)) == ["P4:"], "pr P4: a marker line that names another key fails")
        t.ok(codes(back, mark(k1)) == ["P5:"], "pr P5: bringing back a replaced key fails")
        t.ok(codes(gone, mark(k2)) == ["P1:"], "pr P1: deleting the key fails")
        t.ok(codes(plain, mark(k3)) == ["P6:"], "pr P6: a marker line without a change of the key fails")
        git(root, "checkout", "-q", "main")
        later = commit("main moves on")
        t.ok(pr(root, later, rotate, mark(k3)) == [], "pr: the change is judged from the merge base when main has moved on")
        try:
            pr(root, base, "0" * 40, "")
            t.ok(False, "pr: an unknown commit is unreadable")
        except Unreadable:
            t.ok(True, "pr: an unknown commit is unreadable")
    if t.failed:
        for f in t.failed:
            print("%s: self-test FAILED: %s" % (NAME, f))
        return 1
    print("%s: self-test ok, %d cases passed" % (NAME, t.n))
    return 0


def main(argv):
    if argv == ["--self-test"]:
        return self_test()
    if argv == ["tree"]:
        out = tree(".")
        ok = "%s: %s is a P-256 public key and %s records its SHA-256" % (NAME, KEY, DOC)
    elif len(argv) == 7 and argv[0] == "pr" and argv[1::2] == ["--base", "--head", "--body-file"]:
        try:
            body = open(argv[6], encoding="utf-8").read()
            out = pr(".", argv[2], argv[4], body)
        except (OSError, Unreadable) as e:
            print("%s: cannot read: %s" % (NAME, e))
            return 2
        ok = "%s: no unmarked change of %s" % (NAME, KEY)
    else:
        print("usage: check-release-key.py tree | pr --base B --head H --body-file F | --self-test")
        return 2
    for x in out:
        print("%s: %s" % (NAME, x))
    if out:
        return 1
    print(ok)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
