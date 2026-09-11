package api

import (
	"net/http"
	"strings"

	"github.com/openova-io/openova/products/chargeback/internal/access"
	"github.com/openova-io/openova/products/chargeback/internal/capacity"
	"github.com/openova-io/openova/products/chargeback/internal/rating"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Capacity management (DESIGN.md §11, EPIC #6867).
//
//	GET    /capacity/overview[?region=]     regions → zones → pools + SKU headroom, unmapped SKUs/regions
//	GET    /capacity/regions                regions with their zones
//	POST   /capacity/regions                {code, name, cloud_source_kind?}
//	DELETE /capacity/regions/{id}
//	POST   /capacity/regions/{id}/zones     {code, name, default?} — creates the zone's seven pools at 0
//	DELETE /capacity/zones/{id}
//	GET    /capacity/zones/{id}/pools       the zone, its pools and each pool's total history
//	PUT    /capacity/pools/{id}             {total, note}
//	GET    /capacity/footprints             stored footprints + the family list + the unseeded list-price SKUs
//	PUT    /capacity/footprints/{sku}       {families: {family: amount}} — PUT semantics; {} removes
//	GET    /capacity/caps
//	PUT    /capacity/caps                   {zone_id, sku, total} — total null removes the cap
//
// Reads need metering.read at the Sovereign (a customer principal is 403:
// capacity is the operator's picture of the cloud, not a customer's bill);
// writes need capacity.manage. Every write is audited.

// capacityGrowth is the store's CapacityGrowth over the explorer's run-rate
// arithmetic: a pool's daily consumption is the series, rating.RunRate's
// trend (the least-squares slope over the last seven complete days) is its
// growth in units per day. The store cannot import rating (rating imports
// store), so the API supplies it — the same function the forecast is built
// from, not a second one.
func capacityGrowth(days []store.CapacityDayPoint) (float64, bool) {
	serie := make([]rating.DayCost, 0, len(days))
	for _, d := range days {
		serie = append(serie, rating.DayCost{Day: d.Day, Cost: decFloat(d.Consumed)})
	}
	_, trend, ok := rating.RunRate(serie)
	return trend, ok
}

func (h *Handler) capacityOverview(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.MeteringRead); !ok {
		return
	}
	doc, err := h.Store.CapacityOverview(r.Context(), h.Now(), r.URL.Query().Get("region"), capacityGrowth)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func (h *Handler) listCapacityRegions(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.MeteringRead); !ok {
		return
	}
	list, err := h.Store.ListCapacityRegions(r.Context())
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"regions": list})
}

type capacityRegionBody struct {
	Code            string `json:"code"`
	Name            string `json:"name"`
	CloudSourceKind string `json:"cloud_source_kind"`
}

func (h *Handler) createCapacityRegion(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.CapacityManage); !ok {
		return
	}
	var in capacityRegionBody
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if strings.TrimSpace(in.Code) == "" {
		writeErr(w, http.StatusBadRequest, "code is required: the region as the ledger names it, e.g. me-east-215")
		return
	}
	reg, err := h.Store.CreateCapacityRegion(r.Context(), in.Code, in.Name, in.CloudSourceKind)
	if err != nil {
		if store.IsConflict(err) {
			writeErr(w, http.StatusConflict, "a region with code "+strings.ToLower(strings.TrimSpace(in.Code))+" already exists")
			return
		}
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "capacity.region", map[string]any{"op": "create", "id": reg.ID, "code": reg.Code, "name": reg.Name, "cloud_source_kind": reg.CloudSourceKind})
	writeJSON(w, http.StatusCreated, reg)
}

func (h *Handler) deleteCapacityRegion(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.CapacityManage); !ok {
		return
	}
	id := r.PathValue("id")
	reg, err := h.Store.GetCapacityRegion(r.Context(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	if err := h.Store.DeleteCapacityRegion(r.Context(), id); err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "capacity.region", map[string]any{"op": "delete", "id": id, "code": reg.Code, "zones": len(reg.Zones)})
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id})
}

type capacityZoneBody struct {
	Code    string `json:"code"`
	Name    string `json:"name"`
	Default bool   `json:"default"`
}

func (h *Handler) createCapacityZone(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.CapacityManage); !ok {
		return
	}
	var in capacityZoneBody
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if strings.TrimSpace(in.Code) == "" {
		writeErr(w, http.StatusBadRequest, "code is required: the availability zone as the cloud names it, e.g. me-east-215a")
		return
	}
	z, err := h.Store.CreateCapacityZone(r.Context(), r.PathValue("id"), in.Code, in.Name, in.Default)
	if err != nil {
		if store.IsConflict(err) {
			writeErr(w, http.StatusConflict, "a zone with code "+strings.ToLower(strings.TrimSpace(in.Code))+" already exists in this region")
			return
		}
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "capacity.zone", map[string]any{"op": "create", "id": z.ID, "region": z.RegionCode, "code": z.Code, "name": z.Name, "default": z.IsDefault, "pools": len(z.Pools)})
	writeJSON(w, http.StatusCreated, z)
}

func (h *Handler) deleteCapacityZone(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.CapacityManage); !ok {
		return
	}
	id := r.PathValue("id")
	z, err := h.Store.GetCapacityZone(r.Context(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	if err := h.Store.DeleteCapacityZone(r.Context(), id); err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "capacity.zone", map[string]any{"op": "delete", "id": id, "region": z.RegionCode, "code": z.Code})
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id})
}

// listCapacityPools — the zone, its pools in family order, and each pool's
// total history (newest first), so the page can show who set a total when.
func (h *Handler) listCapacityPools(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.MeteringRead); !ok {
		return
	}
	z, err := h.Store.GetCapacityZone(r.Context(), r.PathValue("id"))
	if err != nil {
		storeErr(w, err)
		return
	}
	history := map[string][]store.CapacityPoolChange{}
	for _, p := range z.Pools {
		hist, err := h.Store.ListCapacityPoolHistory(r.Context(), p.ID, 20)
		if err != nil {
			storeErr(w, err)
			return
		}
		history[p.ID] = hist
	}
	writeJSON(w, http.StatusOK, map[string]any{"zone": z, "pools": z.Pools, "history": history, "families": capacity.Families})
}

type capacityPoolBody struct {
	Total *store.Decimal `json:"total"`
	Note  string         `json:"note"`
}

func (h *Handler) putCapacityPool(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireSovereign(w, r, access.CapacityManage)
	if !ok {
		return
	}
	var in capacityPoolBody
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if in.Total == nil || strings.TrimSpace(string(*in.Total)) == "" {
		writeErr(w, http.StatusBadRequest, "total is required")
		return
	}
	id := r.PathValue("id")
	pool, previous, err := h.Store.SetCapacityPoolTotal(r.Context(), id, *in.Total, in.Note, capacity.SourceManual, s.Email)
	if err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "capacity.pool", map[string]any{"op": "set-total", "id": id, "zone_id": pool.ZoneID, "family": pool.Family, "from": string(previous), "to": string(pool.Total), "note": pool.Note, "source": pool.Source})
	writeJSON(w, http.StatusOK, pool)
}

// listFootprints — the stored footprints (seed + manual), the family list
// the editor offers, and the list-price SKUs that have no footprint by
// construction (so the page can name them instead of asking for one).
func (h *Handler) listFootprints(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.MeteringRead); !ok {
		return
	}
	list, err := h.Store.ListSKUFootprints(r.Context())
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"footprints": list, "families": capacity.Families, "unseeded_skus": capacity.Unseeded()})
}

type footprintBody struct {
	Families map[string]store.Decimal `json:"families"`
}

func (h *Handler) putFootprint(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.CapacityManage); !ok {
		return
	}
	sku := strings.TrimSpace(r.PathValue("sku"))
	var in footprintBody
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if in.Families == nil {
		writeErr(w, http.StatusBadRequest, "families is required: {\"families\": {\"vcpu\": 8, \"memory_gib\": 64}}; an empty object removes the footprint")
		return
	}
	fp, err := h.Store.PutSKUFootprint(r.Context(), sku, in.Families)
	if err != nil {
		storeErr(w, err)
		return
	}
	fams := map[string]string{}
	for k, v := range fp.Families {
		fams[k] = string(v)
	}
	h.audit(r, nil, "capacity.footprint", map[string]any{"op": "put", "sku": sku, "families": fams})
	writeJSON(w, http.StatusOK, fp)
}

func (h *Handler) listCaps(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.MeteringRead); !ok {
		return
	}
	list, err := h.Store.ListSKUCaps(r.Context())
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"caps": list})
}

type capBody struct {
	ZoneID string         `json:"zone_id"`
	SKU    string         `json:"sku"`
	Total  *store.Decimal `json:"total"`
}

// putCap upserts a direct per-SKU cap in a zone; total null (or absent)
// removes the cap.
func (h *Handler) putCap(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireSovereign(w, r, access.CapacityManage)
	if !ok {
		return
	}
	var in capBody
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	in.ZoneID, in.SKU = strings.TrimSpace(in.ZoneID), strings.TrimSpace(in.SKU)
	if in.ZoneID == "" || in.SKU == "" {
		writeErr(w, http.StatusBadRequest, "zone_id and sku are required")
		return
	}
	if in.Total == nil || strings.TrimSpace(string(*in.Total)) == "" {
		if err := h.Store.DeleteSKUCap(r.Context(), in.ZoneID, in.SKU); err != nil {
			storeErr(w, err)
			return
		}
		h.audit(r, nil, "capacity.cap", map[string]any{"op": "delete", "zone_id": in.ZoneID, "sku": in.SKU})
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "zone_id": in.ZoneID, "sku": in.SKU})
		return
	}
	c, err := h.Store.PutSKUCap(r.Context(), in.ZoneID, in.SKU, *in.Total, s.Email)
	if err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "capacity.cap", map[string]any{"op": "put", "zone_id": c.ZoneID, "zone": c.ZoneCode, "region": c.RegionCode, "sku": c.SKU, "total": string(c.Total)})
	writeJSON(w, http.StatusOK, c)
}
