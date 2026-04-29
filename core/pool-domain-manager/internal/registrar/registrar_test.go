package registrar

import (
	"context"
	"errors"
	"testing"
)

type fake struct{ name string }

func (f *fake) Name() string { return f.name }
func (f *fake) ValidateToken(ctx context.Context, token, domain string) error {
	return nil
}
func (f *fake) SetNameservers(ctx context.Context, token, domain string, ns []string) error {
	return nil
}
func (f *fake) GetNameservers(ctx context.Context, token, domain string) ([]string, error) {
	return nil, nil
}

func TestRegistryLookup(t *testing.T) {
	reg := Registry{
		"cloudflare": &fake{name: "cloudflare"},
		"godaddy":    &fake{name: "godaddy"},
	}
	if got, err := reg.Lookup("cloudflare"); err != nil || got.Name() != "cloudflare" {
		t.Fatalf("Lookup(cloudflare) = %v,%v", got, err)
	}
	if _, err := reg.Lookup("nope"); !errors.Is(err, ErrUnsupportedRegistrar) {
		t.Fatalf("Lookup(nope) err = %v, want ErrUnsupportedRegistrar", err)
	}
	var nilReg Registry
	if _, err := nilReg.Lookup("cloudflare"); !errors.Is(err, ErrUnsupportedRegistrar) {
		t.Fatalf("nil Registry.Lookup err = %v, want ErrUnsupportedRegistrar", err)
	}
}

func TestRegistryNamesSorted(t *testing.T) {
	reg := Registry{
		"ovh":        &fake{name: "ovh"},
		"cloudflare": &fake{name: "cloudflare"},
		"godaddy":    &fake{name: "godaddy"},
		"namecheap":  &fake{name: "namecheap"},
		"dynadot":    &fake{name: "dynadot"},
	}
	got := reg.Names()
	want := []string{"cloudflare", "dynadot", "godaddy", "namecheap", "ovh"}
	if len(got) != len(want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("Names()[%d] = %q, want %q (full %v)", i, got[i], want[i], got)
		}
	}
}
