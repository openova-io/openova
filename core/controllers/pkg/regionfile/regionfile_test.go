package regionfile

import (
	"strings"
	"testing"
)

const sample = `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: wp
  namespace: acme
spec:
  values:
    openova:
      topology:
        mode: active-hot-standby
        self: hw-a
        primary: hw-a
        peers: [hw-b]
---
apiVersion: dr.openova.io/v1
kind: Continuum
metadata:
  name: dr-wp
  namespace: acme
spec:
  applicationRef: acme/wp
`

func TestTransitionFields_RoundTrip(t *testing.T) {
	objs, err := Parse([]byte(sample))
	if err != nil || len(objs) != 2 {
		t.Fatalf("parse: %v (%d)", err, len(objs))
	}
	f, ok, err := ReadTransitionFields(objs)
	if err != nil || !ok || f.Primary != "hw-a" || f.PromotionToken != "" {
		t.Fatalf("read: %+v %v %v", f, ok, err)
	}
	n, err := SetTransitionFields(objs, TransitionFields{Primary: "hw-b", PromotionToken: "TOK"})
	if err != nil || n != 1 {
		t.Fatalf("set: %d %v", n, err)
	}
	out, err := Marshal(objs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "promotionToken: TOK") || !strings.Contains(string(out), "primary: hw-b") {
		t.Fatalf("fields not written:\n%s", out)
	}
	if !strings.Contains(string(out), "kind: Continuum") {
		t.Fatalf("non-HelmRelease document dropped:\n%s", out)
	}
	// Byte-stable: parse + marshal again is identical.
	again, _ := Parse(out)
	out2, _ := Marshal(again)
	if string(out) != string(out2) {
		t.Fatalf("marshal is not byte-stable")
	}
	// Clearing the token removes the key.
	if _, err := SetTransitionFields(again, TransitionFields{Primary: "hw-b"}); err != nil {
		t.Fatal(err)
	}
	out3, _ := Marshal(again)
	if strings.Contains(string(out3), "promotionToken") {
		t.Fatalf("empty token must remove the key:\n%s", out3)
	}
}

func TestPaths(t *testing.T) {
	if AppPath("hw-a", "wp") != "regions/hw-a/apps/wp.yaml" {
		t.Fatal(AppPath("hw-a", "wp"))
	}
	if !strings.HasPrefix(LeasePath("g"), "continuum/") {
		t.Fatal("lease file must live outside regions/ so no Flux applies it")
	}
}
