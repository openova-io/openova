package main

import (
	"log/slog"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/openova-io/openova/core/controllers/pkg/upstream"
)

// settablePath is docs/contracts/c5-kit-keys-settable.txt, read from this directory.
const settablePath = "../../../../../../docs/contracts/c5-kit-keys-settable.txt"

// The embedded list is a byte copy of the contract file, and every key it
// lists is a kit key of contract C5.
func TestSettableKitKeysCopy(t *testing.T) {
	want, err := os.ReadFile(settablePath)
	if err != nil {
		t.Fatalf("read %s: %v", settablePath, err)
	}
	if settableKitKeysText != string(want) {
		t.Fatalf("cmd/api/contract/c5-kit-keys-settable.txt differs from %s; copy the contract file", settablePath)
	}
	if len(upstream.KitKeys()) == 0 {
		t.Fatal("upstream.KitKeys() is empty: the kit key list did not load")
	}
	if _, err := settableKitKeys(settableKitKeysText, upstream.KitKeys()); err != nil {
		t.Fatalf("the settable list is refused: %v", err)
	}
}

func TestSettableKitKeysRefusesAKeyOutsideTheKit(t *testing.T) {
	got, err := settableKitKeys("# c\nGPU_RUNTIME\n", []string{"GPU_RUNTIME", "MODEL_X"})
	if err != nil || !reflect.DeepEqual(got, map[string]bool{"GPU_RUNTIME": true}) {
		t.Fatalf("got %v, %v", got, err)
	}
	if _, err := settableKitKeys("NOT_A_KIT_KEY\n", []string{"GPU_RUNTIME"}); err == nil || !strings.Contains(err.Error(), "NOT_A_KIT_KEY") {
		t.Fatalf("a key outside the kit: error %v", err)
	}
}

func TestNewKitKeysRoute(t *testing.T) {
	env := map[string]string{"CATALYST_GITEA_URL": "http://gitea.invalid:3000", "CATALYST_GITEA_TOKEN": "t", "SOVEREIGN_FQDN": " sov.example.test "}
	route := newKitKeysRoute(slog.Default(), func(k string) string { return env[k] })
	if route.Git == nil || route.FQDN != "sov.example.test" {
		t.Fatalf("configured: Git %v, FQDN %q", route.Git, route.FQDN)
	}
	delete(env, "CATALYST_GITEA_TOKEN")
	if route := newKitKeysRoute(slog.Default(), func(k string) string { return env[k] }); route.Git != nil {
		t.Fatalf("without a token the route has Git %v, want none", route.Git)
	}
}
