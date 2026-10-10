package api

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/openova-io/openova/products/chargeback/internal/access"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// ICONS AND BRANDING (DESIGN.md §22.10). Every visual the storefront's
// package table shows is defined here and published through the packages
// document; the storefront renders what the document names and hardcodes
// nothing.
//
//	POST   /api/v1/icons               rating.manage — raw body, Content-Type
//	                                   image/svg+xml | image/png | image/webp,
//	                                   at most 64 KiB → 201 {id, src} (200 when
//	                                   the same bytes were already stored)
//	GET    /api/v1/icons               metering.read — the console's picker
//	DELETE /api/v1/icons/{id}          rating.manage — 409 while referenced
//	GET    /api/v1/public/icons/{id}   public — the bytes, immutable
//	PUT    /api/v1/feature-groups/{key} rating.manage — {icon_id}

// iconMaxBodyBytes reads one byte past the cap, so an oversized upload is
// told so rather than silently truncated into a different image.
const iconMaxBodyBytes = store.IconMaxBytes + 1

// iconCSP is the policy an icon is served under: nothing loads, nothing
// runs, inline styles (which SVG uses for its own presentation) apply.
const iconCSP = "default-src 'none'; style-src 'unsafe-inline'; sandbox"

type iconUploaded struct {
	ID          string `json:"id"`
	Src         string `json:"src"`
	ContentType string `json:"content_type"`
	Size        int    `json:"size"`
}

// uploadIcon — POST /api/v1/icons.
func (h *Handler) uploadIcon(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.RatingManage); !ok {
		return
	}
	ct := r.Header.Get("Content-Type")
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil || (mt != store.IconSVG && mt != store.IconPNG && mt != store.IconWebP) {
		writeErr(w, http.StatusUnsupportedMediaType, fmt.Sprintf("send the icon as the raw request body with Content-Type image/svg+xml, image/png or image/webp (got %q)", ct))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, iconMaxBodyBytes))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read the icon: "+err.Error())
		return
	}
	if len(body) > store.IconMaxBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "an icon is at most 64 KiB; this one is larger")
		return
	}
	ic, created, err := h.Store.PutIcon(r.Context(), mt, body)
	if err != nil {
		storeErr(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		h.audit(r, nil, "icon.upload", map[string]any{"id": ic.ID, "content_type": ic.ContentType, "size": ic.Size})
	}
	writeJSON(w, status, iconUploaded{ID: ic.ID, Src: ic.Src, ContentType: ic.ContentType, Size: ic.Size})
}

// listIcons — GET /api/v1/icons: every stored icon without its bytes, with
// how many features, groups and packages show it.
func (h *Handler) listIcons(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireBookReader(w, r); !ok {
		return
	}
	list, err := h.Store.ListIcons(r.Context())
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"icons": list})
}

// deleteIcon — DELETE /api/v1/icons/{id}: refused with 409 and the counts
// while anything shows the icon.
func (h *Handler) deleteIcon(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.RatingManage); !ok {
		return
	}
	id := strings.ToLower(r.PathValue("id"))
	dep, err := h.Store.DeleteIcon(r.Context(), id)
	if err != nil {
		if store.IsConflict(err) {
			writeErrDetails(w, http.StatusConflict, conflictMessage(err), dep)
			return
		}
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "icon.delete", map[string]any{"id": id})
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id})
}

// publicIcon — GET /api/v1/public/icons/{id}: the bytes under the content
// type they were stored with. The id is the hash of the bytes, so the
// response never changes: immutable for a year. nosniff and a sandboxing
// CSP mean a browser opening the URL directly runs nothing even in an SVG.
func (h *Handler) publicIcon(w http.ResponseWriter, r *http.Request) {
	ic, err := h.Store.GetIcon(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "no such icon")
			return
		}
		storeErr(w, err)
		return
	}
	hd := w.Header()
	hd.Set("Content-Type", ic.ContentType)
	hd.Set("Content-Length", strconv.Itoa(len(ic.Bytes)))
	hd.Set("Cache-Control", "public, max-age=31536000, immutable")
	hd.Set("ETag", `"`+ic.ID+`"`)
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Content-Security-Policy", iconCSP)
	// The storefront on another origin embeds it; a page that isolates
	// itself (COEP) still may.
	hd.Set("Cross-Origin-Resource-Policy", "cross-origin")
	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, ic.ID) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	if _, err := w.Write(ic.Bytes); err != nil {
		slog.Warn("write icon", "error", err)
	}
}

// groupWithIcon is a group as GET /features lists it: the heading and the
// icon id an operator set on it (absent when none).
type groupWithIcon struct {
	Key    string `json:"key"`
	Name   string `json:"name"`
	IconID string `json:"icon_id,omitempty"`
}

func (h *Handler) featureGroups(r *http.Request) ([]groupWithIcon, error) {
	icons, err := h.Store.FeatureGroupIcons(r.Context())
	if err != nil {
		return nil, err
	}
	out := make([]groupWithIcon, 0, len(store.FeatureGroups))
	for _, g := range store.FeatureGroups {
		out = append(out, groupWithIcon{Key: g.Key, Name: g.Name, IconID: icons[g.Key]})
	}
	return out, nil
}

// putFeatureGroup — PUT /api/v1/feature-groups/{key} {"icon_id": "<sha256>"
// or "" to clear}. The group names stay the product's.
func (h *Handler) putFeatureGroup(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSovereign(w, r, access.RatingManage); !ok {
		return
	}
	var in struct {
		IconID *string `json:"icon_id"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if in.IconID == nil {
		writeErr(w, http.StatusBadRequest, `body must be {"icon_id": "<icon id>"} — "" removes the group's icon`)
		return
	}
	key := strings.ToLower(r.PathValue("key"))
	id, err := h.Store.PutFeatureGroupIcon(r.Context(), key, *in.IconID)
	if err != nil {
		storeErr(w, err)
		return
	}
	h.audit(r, nil, "feature_group.update", map[string]any{"key": key, "icon_id": id})
	groups, err := h.featureGroups(r)
	if err != nil {
		storeErr(w, err)
		return
	}
	for _, g := range groups {
		if g.Key == key {
			writeJSON(w, http.StatusOK, g)
			return
		}
	}
	writeErr(w, http.StatusNotFound, "not found")
}
