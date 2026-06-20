package gitguard

import (
	"strings"
	"testing"
)

func TestValidateBasePath_Sovereign(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		fqdn     string
		wantErr  bool
		errMatch string
	}{
		{
			name: "ok — canonical sovereign path",
			path: "clusters/otech113.omani.works/org-tenants",
			fqdn: "otech113.omani.works",
		},
		{
			name: "ok — sovereign path with trailing slash",
			path: "clusters/otech113.omani.works/org-tenants/",
			fqdn: "otech113.omani.works",
		},
		{
			name: "ok — sovereign path with subdir",
			path: "clusters/otech113.omani.works/org-tenants/alice",
			fqdn: "otech113.omani.works",
		},
		{
			name:     "reject — contabo path on Sovereign (the alice2 incident)",
			path:     "clusters/contabo-mkt/tenants",
			fqdn:     "otech113.omani.works",
			wantErr:  true,
			errMatch: "refusing to commit to a foreign cluster",
		},
		{
			name:     "reject — different sovereign FQDN",
			path:     "clusters/otechZ.omani.works/org-tenants",
			fqdn:     "otech113.omani.works",
			wantErr:  true,
			errMatch: "refusing to commit to a foreign cluster",
		},
		{
			name:     "reject — empty path",
			path:     "",
			fqdn:     "otech113.omani.works",
			wantErr:  true,
			errMatch: "must be set",
		},
		{
			name:     "reject — absolute path",
			path:     "/clusters/otech113.omani.works/org-tenants",
			fqdn:     "otech113.omani.works",
			wantErr:  true,
			errMatch: "repo-relative",
		},
		{
			name:     "reject — path traversal",
			path:     "clusters/otech113.omani.works/../contabo-mkt/tenants",
			fqdn:     "otech113.omani.works",
			wantErr:  true,
			errMatch: "repo-relative",
		},
		{
			name:     "reject — outside clusters/",
			path:     "products/catalyst",
			fqdn:     "otech113.omani.works",
			wantErr:  true,
			errMatch: "must start with clusters/",
		},
		{
			name:     "reject — prefix-collision (e.g. otech113-evil.omani.works)",
			path:     "clusters/otech113-evil.omani.works/org-tenants",
			fqdn:     "otech113.omani.works",
			wantErr:  true,
			errMatch: "refusing to commit to a foreign cluster",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateBasePath(tc.path, tc.fqdn)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil for path=%q fqdn=%q", tc.path, tc.fqdn)
				}
				if tc.errMatch != "" && !strings.Contains(err.Error(), tc.errMatch) {
					t.Fatalf("error %q does not contain %q", err.Error(), tc.errMatch)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for path=%q fqdn=%q: %v", tc.path, tc.fqdn, err)
			}
		})
	}
}

func TestValidateBasePath_CatalystZero(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{name: "ok — canonical contabo path", path: "clusters/contabo-mkt/tenants"},
		{name: "ok — contabo path with subdir", path: "clusters/contabo-mkt/tenants/alice"},
		{name: "reject — Sovereign-shaped path on contabo", path: "clusters/otech113.omani.works/org-tenants", wantErr: true},
		{name: "reject — non-clusters/ path", path: "products/catalyst", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateBasePath(tc.path, "")
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil for path=%q", tc.path)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error for path=%q: %v", tc.path, err)
			}
		})
	}
}

func TestValidateGitHubToken(t *testing.T) {
	cases := []struct {
		name    string
		token   string
		wantErr bool
	}{
		{name: "ok — empty (warn-and-continue path)", token: ""},
		{name: "ok — real-looking PAT", token: "ghp_aBcDeFgHiJkLmNoPqRsTuVwXyZ1234567890ab"},
		{name: "ok — gitea token shape", token: "5e3f8b6e1234567890abcdef1234567890abcdef"},
		{name: "reject — <placeholder>", token: "<placeholder>", wantErr: true},
		{name: "reject — <changeme>", token: "<changeme>", wantErr: true},
		{name: "reject — PLACEHOLDER substring", token: "secret-PLACEHOLDER-here", wantErr: true},
		{name: "reject — REPLACE_ME substring", token: "REPLACE_ME_BEFORE_PROD", wantErr: true},
		{name: "reject — REPLACEME substring", token: "tokenREPLACEME123", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateGitHubToken(tc.token)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil for token %q", tc.token)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error for token: %v", err)
			}
		})
	}
}
