package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openova-io/openova/core/controllers/pkg/regions"
)

func TestObserveStandbyInPeers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/clusters" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("name") == "blog-db-5ff1066a" {
			_, _ = w.Write([]byte(`{"region":"b","found":true,"ready":true,"primary":false,"phase":"Cluster in healthy state"}`))
			return
		}
		_, _ = w.Write([]byte(`{"region":"b","found":false}`))
	}))
	defer srv.Close()
	reg, err := regions.Parse(`[{"cloudRegion":"a"},{"cloudRegion":"b"}]`)
	if err != nil {
		t.Fatal(err)
	}
	// "{region}" is absent from the template, so every peer resolves to the
	// test server; the local region ("a") is skipped.
	r := &Reconciler{Cfg: Config{Regions: reg, LocalRegionID: "a", PeerObserverURLTemplate: srv.URL}}
	st, ok := r.observeStandbyInPeers(context.Background(), "acme", "blog-db-5ff1066a")
	if !ok || !st.Ready || st.Primary || st.Region != "b" {
		t.Fatalf("standby = %+v ok=%v", st, ok)
	}
	if _, ok := r.observeStandbyInPeers(context.Background(), "acme", "absent"); ok {
		t.Fatalf("absent Cluster reported as found")
	}
}
