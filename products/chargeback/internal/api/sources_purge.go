package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/collector/huawei"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// purgeExcluded removes the usage a source collected for resources its scope
// token now excludes (#6867). Setting a scope stops NEW records for the
// excluded resources, but the hours collected before the scope existed stay
// in the ledger and on every explorer view and draft statement — measured on
// hw307, the bastion's EIP still carried 225 OMR for 1–3 September after
// #6859 had scoped the source. This is the operator's explicit, audited way
// to take those hours off the bill.
//
// The excluded set is recomputed from the source's inventory with the same
// ScopeMatcher the collector uses, so the purge can never disagree with what
// the collector keeps. Operator-only; the customer never rewrites its ledger.
func (h *Handler) purgeExcluded(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireOperator(w, r); !ok {
		return
	}
	src, err := h.Store.GetSource(r.Context(), store.OperatorScope, r.PathValue("id"))
	if err != nil {
		storeErr(w, err)
		return
	}
	matcher := huawei.ScopeMatcher{Token: src.ScopeToken}
	if !matcher.Enabled() {
		writeErr(w, http.StatusBadRequest, "the source has no scope token; nothing is excluded")
		return
	}
	items, err := h.Store.ListInventory(r.Context(), src.ID)
	if err != nil {
		storeErr(w, err)
		return
	}
	resources := make([]huawei.Resource, 0, len(items))
	for _, it := range items {
		attrs := map[string]any{}
		if len(it.Attrs) > 0 {
			_ = json.Unmarshal(it.Attrs, &attrs)
		}
		resources = append(resources, huawei.Resource{ID: it.ResourceID, Kind: it.Kind, Name: it.Name, Attrs: attrs})
	}
	_, excluded := matcher.Partition(resources)
	ids := make([]string, 0, len(excluded))
	kinds := map[string]bool{}
	for _, x := range excluded {
		ids = append(ids, x.ID)
		kinds[x.Kind] = true
	}
	deleted, err := h.Store.DeleteUsageForResources(r.Context(), src.ID, ids)
	if err != nil {
		storeErr(w, err)
		return
	}
	// The excluded resources are gone from this customer's view: mark them
	// deleted in the inventory so the resources page and the recommendations
	// stop listing them (the collector keeps them marked on every pass).
	now := h.Now()
	for _, x := range excluded {
		at := now
		if err := h.Store.SetInventoryBounds(r.Context(), src.ID, x.ID, nil, &at); err != nil {
			storeErr(w, err)
			return
		}
	}
	h.audit(r, &src.CustomerID, "source.purge_excluded", map[string]any{"source_id": src.ID, "scope_token": src.ScopeToken, "resources": len(ids), "usage_records_deleted": deleted})
	writeJSON(w, http.StatusOK, map[string]any{
		"source_id":             src.ID,
		"scope_token":           src.ScopeToken,
		"excluded_resources":    ids,
		"usage_records_deleted": deleted,
		"at":                    now.UTC().Format(time.RFC3339),
	})
}
