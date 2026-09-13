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
//	GET    /capacity/overview[?region=]        regions → zones → pools, by class, with the walls
//	GET    /capacity/regions                   regions with their zones
//	POST   /capacity/regions                   {code, name, cloud_source_kind?}
//	DELETE /capacity/regions/{id}
//	POST   /capacity/regions/{id}/zones        {code, name, default?}
//	DELETE /capacity/zones/{id}
//	GET    /capacity/zones/{id}/pools          the zone, its pools and each pool's history
//	POST   /capacity/zones/{id}/pools          {name, machines, lead_time_days, note, resources[]}
//	PUT    /capacity/pools/{id}                the same body; replaces the pool's size and policy
//	DELETE /capacity/pools/{id}
//	GET    /capacity/pools/{id}/headroom?basket=sku:units,... how many more of a mix fit
//	GET    /capacity/shapes                    stored shapes + resource kinds + the unseeded list-price SKUs
//	PUT    /capacity/shapes/{sku}              {resources: {resource: amount}} — PUT semantics; {} removes
//	GET    /capacity/placements                every (sku, pool, class)
//	PUT    /capacity/placements                {pool_id, sku, class} — class null removes the placement
//	GET    /capacity/resources                 the resource kinds
//	PUT    /capacity/resources/{resource}      {label, unit}
//
// Reads need metering.read at the Sovereign (a customer principal is 403:
// capacity is the operator's picture of the cloud, not a customer's bill);
// writes need capacity.manage. Every write is audited.

// capacityGrowth is the store's CapacityGrowth over the explorer's run-rate
// arithmetic: a class's daily consumption of a pool's resource is the series,
// rating.RunRate's trend (the least-squares slope over the last seven
// complete days) is its growth in units per day. The store cannot import
// rating (rating imports store), so the API supplies it — the same function
// the forecast is built from, not a second one.
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
	h.audit(r, nil, "capacity.zone", map[string]any{"op": "create", "id": z.ID, "region": z.RegionCode, "code": z.Code, "name": z.Name, "default": z.IsDefault})
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
	h.audit(r, nil, "capacity.zone", map[string]any{"op": "delete", "id": id, "region": z.RegionCode, "code": z.Code, "pools": len(z.Pools)})
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id})
}

// listCapacityPools — the zone, its pools with their resource vectors, and
// each pool's size history (newest first), so the page can show who changed
// what when.
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
		hist, err := h.Store.ListCapacityPoolHistory(r.Context(), p.ID, 40)
		if err != nil {
			storeErr(w, err)
			return
		}
		history[p.ID] = hist
	}
	kinds, err := h.Store.ListCapacityResourceKinds(r.Context())
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"zone": z, "pools": z.Pools, "history": history, "resource_kinds": kinds, "classes": capacity.Classes})
}

// poolSummary is what an audit entry records about a pool: its name, machine
// count, lead time and the per-machine vector with its policy.
func poolSummary(p store.CapacityPool) map[string]any {
	res := map[string]any{}
	for _, r := range p.Resources {
		res[r.Resource] = map[string]string{"per_machine": string(r.PerMachine), "reserve": string(r.Reserve), "overcommit_ratio": string(r.OvercommitRatio)}
	}
	return map[string]any{"name": p.Name, "machines": string(p.Machines), "lead_time_days": p.LeadTimeDays, "resources": res}
}

func (h *Handler) createCapacityPool(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireSovereign(w, r, access.CapacityManage)
	if !ok {
		return
	}
	var in store.CapacityPoolInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	pool, err := h.Store.CreateCapacityPool(r.Context(), r.PathValue("id"), in, s.Email)
	if err != nil {
		if store.IsConflict(err) {
			writeErr(w, http.StatusConflict, "a pool named "+strings.TrimSpace(in.Name)+" already exists in this zone")
			return
		}
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "capacity.pool", map[string]any{"op": "create", "id": pool.ID, "zone_id": pool.ZoneID, "zone": pool.ZoneCode, "region": pool.RegionCode, "to": poolSummary(pool), "note": pool.Note})
	writeJSON(w, http.StatusCreated, pool)
}

func (h *Handler) putCapacityPool(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireSovereign(w, r, access.CapacityManage)
	if !ok {
		return
	}
	var in store.CapacityPoolInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	id := r.PathValue("id")
	pool, previous, err := h.Store.SetCapacityPool(r.Context(), id, in, s.Email)
	if err != nil {
		if store.IsConflict(err) {
			writeErr(w, http.StatusConflict, "a pool named "+strings.TrimSpace(in.Name)+" already exists in this zone")
			return
		}
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "capacity.pool", map[string]any{"op": "set", "id": id, "zone_id": pool.ZoneID, "zone": pool.ZoneCode, "region": pool.RegionCode,
		"from": poolSummary(previous), "to": poolSummary(pool), "note": pool.Note, "source": pool.Source})
	writeJSON(w, http.StatusOK, pool)
}

func (h *Handler) deleteCapacityPool(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.CapacityManage); !ok {
		return
	}
	id := r.PathValue("id")
	pool, err := h.Store.GetCapacityPool(r.Context(), id)
	if err != nil {
		storeErr(w, err)
		return
	}
	if err := h.Store.DeleteCapacityPool(r.Context(), id); err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "capacity.pool", map[string]any{"op": "delete", "id": id, "zone_id": pool.ZoneID, "zone": pool.ZoneCode, "region": pool.RegionCode, "from": poolSummary(pool)})
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id})
}

// capacityHeadroom answers "how many more of this mix fit on this pool" for a
// mix the operator names as basket=sku:units,sku:units. Without a basket it
// answers for the mix currently selling, which is what the overview shows.
func (h *Handler) capacityHeadroom(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.MeteringRead); !ok {
		return
	}
	req := store.ParseBasket(r.URL.Query().Get("basket"))
	pv, err := h.Store.CapacityPoolBasket(r.Context(), r.PathValue("id"), h.Now(), req, capacityGrowth)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pool": pv, "basket": pv.Basket})
}

// listCapacityShapes — the stored shapes (seed + manual), the resource kinds
// the editor offers, and the list-price SKUs that have no shape by
// construction (so the page can name them instead of asking for one).
func (h *Handler) listCapacityShapes(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.MeteringRead); !ok {
		return
	}
	list, err := h.Store.ListCapacityShapes(r.Context())
	if err != nil {
		storeErr(w, err)
		return
	}
	kinds, err := h.Store.ListCapacityResourceKinds(r.Context())
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"shapes": list, "resource_kinds": kinds, "unseeded_skus": capacity.Unseeded()})
}

type shapeBody struct {
	Resources map[string]store.Decimal `json:"resources"`
}

func (h *Handler) putCapacityShape(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.CapacityManage); !ok {
		return
	}
	sku := strings.TrimSpace(r.PathValue("sku"))
	var in shapeBody
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if in.Resources == nil {
		writeErr(w, http.StatusBadRequest, "resources is required: {\"resources\": {\"vcpu\": 8, \"memory_gib\": 64}}; an empty object removes the shape")
		return
	}
	sh, err := h.Store.PutCapacityShape(r.Context(), sku, in.Resources)
	if err != nil {
		storeErr(w, err)
		return
	}
	res := map[string]string{}
	for k, v := range sh.Resources {
		res[k] = string(v)
	}
	h.audit(r, nil, "capacity.shape", map[string]any{"op": "put", "sku": sku, "resources": res})
	writeJSON(w, http.StatusOK, sh)
}

func (h *Handler) listCapacityPlacements(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.MeteringRead); !ok {
		return
	}
	list, err := h.Store.ListCapacityPlacements(r.Context())
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"placements": list, "classes": capacity.Classes})
}

type placementBody struct {
	PoolID string  `json:"pool_id"`
	SKU    string  `json:"sku"`
	Class  *string `json:"class"`
}

// putCapacityPlacement places a SKU on a pool at a class; class null (or
// absent) removes the placement.
func (h *Handler) putCapacityPlacement(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireSovereign(w, r, access.CapacityManage)
	if !ok {
		return
	}
	var in placementBody
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	in.PoolID, in.SKU = strings.TrimSpace(in.PoolID), strings.TrimSpace(in.SKU)
	if in.PoolID == "" || in.SKU == "" {
		writeErr(w, http.StatusBadRequest, "pool_id and sku are required")
		return
	}
	if in.Class == nil || strings.TrimSpace(*in.Class) == "" {
		if err := h.Store.DeleteCapacityPlacement(r.Context(), in.PoolID, in.SKU); err != nil {
			storeErr(w, err)
			return
		}
		h.audit(r, nil, "capacity.placement", map[string]any{"op": "delete", "pool_id": in.PoolID, "sku": in.SKU})
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "pool_id": in.PoolID, "sku": in.SKU})
		return
	}
	pl, err := h.Store.PutCapacityPlacement(r.Context(), in.PoolID, in.SKU, *in.Class, s.Email)
	if err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "capacity.placement", map[string]any{"op": "put", "pool_id": pl.PoolID, "pool": pl.PoolName, "zone": pl.ZoneCode, "region": pl.RegionCode, "sku": pl.SKU, "class": pl.Class})
	writeJSON(w, http.StatusOK, pl)
}

func (h *Handler) listCapacityResourceKinds(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.MeteringRead); !ok {
		return
	}
	list, err := h.Store.ListCapacityResourceKinds(r.Context())
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"resource_kinds": list, "classes": capacity.Classes})
}

type resourceKindBody struct {
	Label string `json:"label"`
	Unit  string `json:"unit"`
}

// putCapacityResourceKind names a resource kind. There is no list to be on:
// this only says how a key is LABELLED and what unit its amounts count in.
func (h *Handler) putCapacityResourceKind(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.CapacityManage); !ok {
		return
	}
	var in resourceKindBody
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	k, err := h.Store.PutCapacityResourceKind(r.Context(), r.PathValue("resource"), in.Label, in.Unit)
	if err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "capacity.resource", map[string]any{"op": "put", "resource": k.Key, "label": k.Label, "unit": k.Unit})
	writeJSON(w, http.StatusOK, k)
}
