package handlers

import "net/http"

// Routes returns an http.Handler with all catalog endpoints registered.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()

	// Public — read-only catalog browsing.
	mux.HandleFunc("GET /catalog/apps", h.ListApps)
	mux.HandleFunc("GET /catalog/apps/search", h.SearchApps)
	mux.HandleFunc("GET /catalog/apps/{slug}", h.GetApp)
	mux.HandleFunc("GET /catalog/industries", h.ListIndustries)
	mux.HandleFunc("GET /catalog/bundles", h.ListBundles)
	mux.HandleFunc("GET /catalog/bundles/{slug}", h.GetBundle)
	mux.HandleFunc("GET /catalog/plans", h.ListPlans)
	mux.HandleFunc("GET /catalog/addons", h.ListAddOns)

	// Admin — mutating operations (require superadmin JWT).
	mux.HandleFunc("POST /catalog/admin/apps", h.CreateApp)
	mux.HandleFunc("PUT /catalog/admin/apps/{id}", h.UpdateApp)
	mux.HandleFunc("DELETE /catalog/admin/apps/{id}", h.DeleteApp)
	mux.HandleFunc("POST /catalog/admin/industries", h.CreateIndustry)
	mux.HandleFunc("PUT /catalog/admin/industries/{id}", h.UpdateIndustry)
	mux.HandleFunc("DELETE /catalog/admin/industries/{id}", h.DeleteIndustry)
	mux.HandleFunc("POST /catalog/admin/bundles", h.CreateBundle)
	mux.HandleFunc("PUT /catalog/admin/bundles/{id}", h.UpdateBundle)
	mux.HandleFunc("DELETE /catalog/admin/bundles/{id}", h.DeleteBundle)
	mux.HandleFunc("POST /catalog/admin/plans", h.CreatePlan)
	mux.HandleFunc("PUT /catalog/admin/plans/{id}", h.UpdatePlan)
	mux.HandleFunc("DELETE /catalog/admin/plans/{id}", h.DeletePlan)
	mux.HandleFunc("POST /catalog/admin/addons", h.CreateAddOn)
	mux.HandleFunc("PUT /catalog/admin/addons/{id}", h.UpdateAddOn)
	mux.HandleFunc("DELETE /catalog/admin/addons/{id}", h.DeleteAddOn)

	return mux
}
