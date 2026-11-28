package config

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/crypto"
	"github.com/openova-io/openova/products/chargeback/internal/secretfile"
)

// The projected-token path env was renamed PLATFORM_API_TOKEN_FILE →
// PLATFORM_API_BEARER_FILE (the Sovereign's Kyverno `secret-not-in-env`
// policy flags a TOKEN-named env carrying a literal value). Both names are
// read for one release; the new one wins when both are set.
func TestPlatformAPIBearerFileEnvAndDeprecatedAlias(t *testing.T) {
	cases := []struct {
		name, bearer, legacy, want string
	}{
		{"new name only", "/run/new", "", "/run/new"},
		{"deprecated alias only", "", "/run/old", "/run/old"},
		{"both set — new name wins", "/run/new", "/run/old", "/run/new"},
		{"neither", "", "", ""},
		{"whitespace-only new name falls back to the alias", "  ", "/run/old", "/run/old"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PLATFORM_API_BEARER_FILE", tc.bearer)
			t.Setenv("PLATFORM_API_TOKEN_FILE", tc.legacy)
			c, err := FromEnv()
			if err != nil {
				t.Fatal(err)
			}
			if c.PlatformAPITokenFile != tc.want {
				t.Fatalf("PlatformAPITokenFile = %q, want %q", c.PlatformAPITokenFile, tc.want)
			}
		})
	}
}

// DEFAULT_OFFER (DESIGN.md §22.12) is on by default for the sovereign profile
// — a fresh Sovereign must publish the offer the engine ships with — and off
// for operator-central, which sells no packages. An explicit value wins.
func TestDefaultOfferOnByDefaultForTheSovereignProfile(t *testing.T) {
	cases := []struct {
		profile, env string
		want         bool
	}{
		{"", "", true},
		{"sovereign", "", true},
		{"operator-central", "", false},
		{"sovereign", "false", false},
		{"operator-central", "true", true},
	}
	for _, tc := range cases {
		t.Run(tc.profile+"/"+tc.env, func(t *testing.T) {
			t.Setenv("PROFILE", tc.profile)
			t.Setenv("DEFAULT_OFFER", tc.env)
			c, err := FromEnv()
			if err != nil {
				t.Fatal(err)
			}
			if c.DefaultOffer != tc.want {
				t.Fatalf("PROFILE=%q DEFAULT_OFFER=%q: DefaultOffer = %v, want %v", tc.profile, tc.env, c.DefaultOffer, tc.want)
			}
		})
	}
}

// useMount points the mount directory of package secretfile at a fresh
// directory for one test, holding the master key file with content when
// content is not nil.
func useMount(t *testing.T, content *string) string {
	t.Helper()
	d := t.TempDir()
	old := secretfile.Dir
	secretfile.Dir = d
	t.Cleanup(func() { secretfile.Dir = old })
	if content != nil {
		if err := os.WriteFile(filepath.Join(d, EncryptionKeyFile), []byte(*content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func ptr(s string) *string { return &s }

// SEC-246: on a Sovereign the master key is the file the Secrets Store CSI
// driver mounts; the variable is not read when the file exists.
func TestMasterKeyIsTheMountedFile(t *testing.T) {
	useMount(t, ptr("key-from-the-file\n"))
	t.Setenv("APP_ENCRYPTION_KEY", "key-from-the-variable")
	c, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if c.EncryptionKeyB64 != "key-from-the-file" {
		t.Fatalf("EncryptionKeyB64 = %q, want the file's content key-from-the-file", c.EncryptionKeyB64)
	}
}

// A local run mounts nothing: the key is APP_ENCRYPTION_KEY, as before.
func TestMasterKeyIsTheVariableWhenNothingIsMounted(t *testing.T) {
	useMount(t, nil)
	t.Setenv("APP_ENCRYPTION_KEY", "key-from-the-variable")
	c, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if c.EncryptionKeyB64 != "key-from-the-variable" {
		t.Fatalf("EncryptionKeyB64 = %q, want key-from-the-variable", c.EncryptionKeyB64)
	}
}

// A mounted file that is empty fails the start, and the variable does not
// stand in for it.
func TestMasterKeyEmptyFileFailsEvenWithTheVariable(t *testing.T) {
	d := useMount(t, ptr("\n"))
	t.Setenv("APP_ENCRYPTION_KEY", "key-from-the-variable")
	c, err := FromEnv()
	if !errors.Is(err, secretfile.ErrEmpty) {
		t.Fatalf("FromEnv with an empty key file: error %v (key %q), want secretfile.ErrEmpty", err, c.EncryptionKeyB64)
	}
	if want := filepath.Join(d, EncryptionKeyFile); !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not name the file %s", err, want)
	}
}

// The values the store holds for the key, read from the file into a keyring:
// the value seeded from the Secret the old Job wrote (base64 of 32 bytes, 44
// characters with padding) and a fresh value of the birth Job (43 letters and
// digits, which decode to 32 bytes). The library's default length, 32
// characters, decodes to 24 bytes and is refused.
func TestMasterKeyFileValuesMakeAKeyring(t *testing.T) {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(7*i + 3)
	}
	seeded := base64.StdEncoding.EncodeToString(raw)
	fresh := strings.Repeat("Az09bY8xCw7v", 4)[:43]
	if len(seeded) != 44 || len(fresh) != 43 {
		t.Fatalf("fixture lengths %d and %d, want 44 and 43", len(seeded), len(fresh))
	}
	for _, v := range []string{seeded, fresh} {
		useMount(t, ptr(v+"\n"))
		t.Setenv("APP_ENCRYPTION_KEY", "")
		c, err := FromEnv()
		if err != nil {
			t.Fatal(err)
		}
		if c.EncryptionKeyB64 != v {
			t.Fatalf("EncryptionKeyB64 = %q, want %q", c.EncryptionKeyB64, v)
		}
		if _, err := crypto.NewKeyring(c.EncryptionKeyB64); err != nil {
			t.Fatalf("a key file of %d characters: %v", len(v), err)
		}
	}
	useMount(t, ptr(fresh[:32]))
	c, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := crypto.NewKeyring(c.EncryptionKeyB64); !errors.Is(err, crypto.ErrKeyLength) {
		t.Fatalf("a key file of 32 characters: error %v, want crypto.ErrKeyLength", err)
	}
}
