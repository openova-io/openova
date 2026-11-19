package hetzner

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestPurge_NeverDeletesAnAttachedVolume — the wipe of the Hetzner adapter
// (Purge) deletes the Sovereign's detached volumes and never a volume that
// is attached to a server, whatever created it: neither in the label sweep
// nor in the name-prefix fallback (CLD-111).
func TestPurge_NeverDeletesAnAttachedVolume(t *testing.T) {
	const fqdn = "attached.omani.works"
	prefix := NamePrefixForSovereign(fqdn)
	server := 7
	labelled := []map[string]any{
		{"id": 1, "name": prefix + "-data-1", "server": nil},
		{"id": 2, "name": prefix + "-data-2", "server": server},
	}
	unlabelled := []map[string]any{
		{"id": 3, "name": prefix + "-pv-3", "server": nil},
		{"id": 4, "name": prefix + "-pv-4", "server": server},
		{"id": 5, "name": "pvc-attached-elsewhere", "server": server},
	}
	var mu sync.Mutex
	var deleted []int
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		kind := parts[1]
		if r.Method == http.MethodDelete {
			if kind == "volumes" {
				var id int
				_, _ = fmt.Sscanf(parts[2], "%d", &id)
				mu.Lock()
				deleted = append(deleted, id)
				mu.Unlock()
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		entries := []map[string]any{}
		if kind == "volumes" {
			if r.URL.Query().Get("label_selector") != "" {
				entries = labelled
			} else {
				entries = append(append(entries, labelled...), unlabelled...)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"meta": map[string]any{"pagination": map[string]any{"next_page": nil}},
			kind:   entries,
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	srvURL, _ := url.Parse(srv.URL)
	origClient, origBackoff := purgeHTTPClient, firewallRetryInitialBackoff
	purgeHTTPClient = &http.Client{Transport: &rewriteTransport{from: "api.hetzner.cloud", to: srvURL, base: http.DefaultTransport}, Timeout: 5 * time.Second}
	firewallRetryInitialBackoff = time.Millisecond
	defer func() { purgeHTTPClient, firewallRetryInitialBackoff = origClient, origBackoff }()

	kept := map[string]bool{} // both passes see a labelled volume
	report, err := Purge(context.Background(), "token", fqdn, func(msg string) {
		if strings.HasPrefix(msg, "kept volume") {
			kept[msg] = true
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(deleted)
	if !slices.Equal(deleted, []int{1, 3}) {
		t.Fatalf("deleted volumes %v, want [1 3]: an attached volume was deleted or a detached one kept", deleted)
	}
	if len(report.Volumes) != 2 || len(kept) != 2 {
		t.Fatalf("report.Volumes=%v kept=%v, want two deleted and the two attached Sovereign volumes kept", report.Volumes, kept)
	}
}
