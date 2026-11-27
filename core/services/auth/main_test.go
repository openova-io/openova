package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/openova-io/openova/core/services/auth/internal/secretfile"
)

// The service reads the file jwt-refresh-secret of the mount, the objectName of
// SecretProviderClass auth (chart test auth-refresh-secret-mounted.sh holds the
// chart side of the same name), and starts on nothing else.
func TestRefreshSecretComesFromTheMountedFile(t *testing.T) {
	d := t.TempDir()
	old := secretfile.Dir
	secretfile.Dir = d
	t.Cleanup(func() { secretfile.Dir = old })
	t.Setenv("JWT_REFRESH_SECRET", "from-the-environment")

	if _, err := readRefreshSecret(); !errors.Is(err, secretfile.ErrNotMounted) {
		t.Fatalf("without the file: error %v, want ErrNotMounted (the variable must not be read)", err)
	}
	if err := os.WriteFile(filepath.Join(d, "jwt-refresh-secret"), []byte("mounted-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readRefreshSecret()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "mounted-value" {
		t.Fatalf("readRefreshSecret = %q, want mounted-value", got)
	}
}
