package secretfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// useDir points Dir at a fresh directory for one test.
func useDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	old := Dir
	Dir = d
	t.Cleanup(func() { Dir = old })
	return d
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMissingFileIsNotMounted(t *testing.T) {
	d := useDir(t)
	v, err := Read("app-encryption-key")
	if !errors.Is(err, ErrNotMounted) {
		t.Fatalf("Read of a missing file: error %v, want ErrNotMounted", err)
	}
	if want := filepath.Join(d, "app-encryption-key"); !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not name the path %s", err, want)
	}
	if v != "" {
		t.Fatalf("Read of a missing file returned %q, want nothing", v)
	}
}

func TestEmptyFileIsRefused(t *testing.T) {
	d := useDir(t)
	for _, content := range []string{"", "\n", "\r\n\n"} {
		write(t, filepath.Join(d, "v"), content)
		v, err := Read("v")
		if !errors.Is(err, ErrEmpty) {
			t.Fatalf("Read of a file holding %q: error %v, want ErrEmpty", content, err)
		}
		if v != "" {
			t.Fatalf("Read of a file holding %q returned %q, want nothing", content, v)
		}
	}
}

func TestTrailingNewlineIsDropped(t *testing.T) {
	d := useDir(t)
	cases := map[string]string{
		"s3cr3t":       "s3cr3t",
		"s3cr3t\n":     "s3cr3t",
		"s3cr3t\r\n":   "s3cr3t",
		"s3cr3t\n\n":   "s3cr3t",
		"line1\nline2": "line1\nline2",
		" padded \n":   " padded ",
	}
	for content, want := range cases {
		write(t, filepath.Join(d, "v"), content)
		got, err := Read("v")
		if err != nil {
			t.Fatalf("Read of a file holding %q: %v", content, err)
		}
		if got != want {
			t.Fatalf("Read of a file holding %q = %q, want %q", content, got, want)
		}
	}
}

// The driver writes as the kubelet's atomic writer does: the file is a link into
// ..data, and a rotation swaps ..data to a new directory in one rename.
func TestRotatedFileIsReadAgain(t *testing.T) {
	d := useDir(t)
	publish := func(gen, value string) {
		t.Helper()
		dir := filepath.Join(d, gen)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(dir, "v"), value)
		tmp := filepath.Join(d, "..data_tmp")
		if err := os.Symlink(gen, tmp); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, filepath.Join(d, "..data")); err != nil {
			t.Fatal(err)
		}
	}
	publish("..2026_11_27_01", "first-value")
	if err := os.Symlink(filepath.Join("..data", "v"), filepath.Join(d, "v")); err != nil {
		t.Fatal(err)
	}
	if got, err := Read("v"); err != nil || got != "first-value" {
		t.Fatalf("Read before the rotation = %q, %v; want first-value", got, err)
	}
	publish("..2026_11_27_02", "second-value\n")
	if got, err := Read("v"); err != nil || got != "second-value" {
		t.Fatalf("Read after the rotation = %q, %v; want second-value", got, err)
	}
}

func TestNameMustBeOneFileName(t *testing.T) {
	d := useDir(t)
	write(t, filepath.Join(d, "v"), "value")
	for _, name := range []string{"", "../v", "sub/v", ".", "..", "..data"} {
		if got, err := Read(name); err == nil {
			t.Fatalf("Read(%q) = %q, want a refusal", name, got)
		} else if errors.Is(err, ErrNotMounted) || errors.Is(err, ErrEmpty) {
			t.Fatalf("Read(%q): %v, want the name refused before any file is read", name, err)
		}
	}
}
