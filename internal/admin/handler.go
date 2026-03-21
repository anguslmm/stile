// Package admin implements the HTTP admin API for managing callers and API keys.
package admin

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/anguslmm/stile/internal/auth"
	"github.com/anguslmm/stile/internal/router"
)

// Handler serves admin API endpoints for caller and key management.
type Handler struct {
	store  auth.Store
	router *router.RouteTable
}

// NewHandler creates an admin handler.
func NewHandler(store auth.Store, rt *router.RouteTable) *Handler {
	return &Handler{store: store, router: rt}
}

// Register registers all admin routes on the given mux.
// All routes should be wrapped with admin auth middleware by the caller.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /admin/callers", h.createCaller)
	mux.HandleFunc("GET /admin/callers", h.listCallers)
	mux.HandleFunc("GET /admin/callers/{name}", h.getCaller)
	mux.HandleFunc("DELETE /admin/callers/{name}", h.deleteCaller)
	mux.HandleFunc("POST /admin/callers/{name}/keys", h.createKey)
	mux.HandleFunc("GET /admin/callers/{name}/keys", h.listKeys)
	mux.HandleFunc("DELETE /admin/callers/{name}/keys/{id}", h.deleteKey)
	mux.HandleFunc("POST /admin/callers/{name}/roles", h.assignRole)
	mux.HandleFunc("GET /admin/callers/{name}/roles", h.listRoles)
	mux.HandleFunc("DELETE /admin/callers/{name}/roles/{role}", h.deleteRole)
	mux.HandleFunc("POST /admin/refresh", h.refresh)
}

func (h *Handler) createCaller(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid JSON"))
		return
	}
	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("name is required"))
		return
	}

	if err := h.store.AddCaller(req.Name); err != nil {
		if errors.Is(err, auth.ErrDuplicate) {
			writeJSON(w, http.StatusConflict, errorBody("caller already exists"))
			return
		}
		writeJSON(w, http.StatusInternalServerError, errorBody("internal error"))
		return
	}

	detail, err := h.store.GetCaller(req.Name)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("internal error"))
		return
	}

	writeJSON(w, http.StatusCreated, callerSummary{
		Name:      detail.Name,
		CreatedAt: detail.CreatedAt,
	})
}

func (h *Handler) listCallers(w http.ResponseWriter, _ *http.Request) {
	callers, err := h.store.ListCallers()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("internal error"))
		return
	}

	items := make([]callerListItem, len(callers))
	for i, c := range callers {
		roles := c.Roles
		if roles == nil {
			roles = []string{}
		}
		items[i] = callerListItem{
			Name:      c.Name,
			KeyCount:  c.KeyCount,
			Roles:     roles,
			CreatedAt: c.CreatedAt,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"callers": items})
}

func (h *Handler) getCaller(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	detail, err := h.store.GetCaller(name)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorBody("caller not found"))
		return
	}

	keys := make([]keyItem, len(detail.Keys))
	for i, k := range detail.Keys {
		keys[i] = keyItem{
			ID:        k.ID,
			Label:     k.Label,
			CreatedAt: k.CreatedAt,
		}
	}

	roles, err := h.store.RolesForCaller(name)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("internal error"))
		return
	}
	if roles == nil {
		roles = []string{}
	}

	writeJSON(w, http.StatusOK, callerDetailResp{
		Name:      detail.Name,
		Keys:      keys,
		Roles:     roles,
		CreatedAt: detail.CreatedAt,
	})
}

func (h *Handler) deleteCaller(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	if err := h.store.DeleteCaller(name); err != nil {
		writeJSON(w, http.StatusNotFound, errorBody("caller not found"))
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) createKey(w http.ResponseWriter, r *http.Request) {
	callerName := r.PathValue("name")

	// Verify caller exists.
	if _, err := h.store.GetCaller(callerName); err != nil {
		writeJSON(w, http.StatusNotFound, errorBody("caller not found"))
		return
	}

	var req struct {
		Label string `json:"label"`
	}
	if r.Body != nil && r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody("invalid JSON"))
			return
		}
	}

	rawKey, err := auth.GenerateAPIKey()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("internal error"))
		return
	}
	hash := sha256.Sum256([]byte(rawKey))

	if err := h.store.AddKey(callerName, hash, req.Label); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("internal error"))
		return
	}

	writeJSON(w, http.StatusCreated, createKeyResp{
		Key:       rawKey,
		Label:     req.Label,
		CreatedAt: time.Now().UTC(),
	})
}

func (h *Handler) listKeys(w http.ResponseWriter, r *http.Request) {
	callerName := r.PathValue("name")

	// Verify caller exists.
	if _, err := h.store.GetCaller(callerName); err != nil {
		writeJSON(w, http.StatusNotFound, errorBody("caller not found"))
		return
	}

	keys, err := h.store.ListKeys(callerName)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("internal error"))
		return
	}

	items := make([]keyItem, len(keys))
	for i, k := range keys {
		items[i] = keyItem{
			ID:        k.ID,
			Label:     k.Label,
			CreatedAt: k.CreatedAt,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": items})
}

func (h *Handler) deleteKey(w http.ResponseWriter, r *http.Request) {
	callerName := r.PathValue("name")
	idStr := r.PathValue("id")

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid key ID"))
		return
	}

	if err := h.store.DeleteKey(callerName, id); err != nil {
		writeJSON(w, http.StatusNotFound, errorBody("key not found"))
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) assignRole(w http.ResponseWriter, r *http.Request) {
	callerName := r.PathValue("name")

	var req struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid JSON"))
		return
	}
	if req.Role == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("role is required"))
		return
	}

	if err := h.store.AssignRole(callerName, req.Role); err != nil {
		if errors.Is(err, auth.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, errorBody("caller not found"))
			return
		}
		writeJSON(w, http.StatusInternalServerError, errorBody("internal error"))
		return
	}

	roles, err := h.store.RolesForCaller(callerName)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("internal error"))
		return
	}
	if roles == nil {
		roles = []string{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"name":  callerName,
		"roles": roles,
	})
}

func (h *Handler) listRoles(w http.ResponseWriter, r *http.Request) {
	callerName := r.PathValue("name")

	// Verify caller exists.
	if _, err := h.store.GetCaller(callerName); err != nil {
		writeJSON(w, http.StatusNotFound, errorBody("caller not found"))
		return
	}

	roles, err := h.store.RolesForCaller(callerName)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("internal error"))
		return
	}
	if roles == nil {
		roles = []string{}
	}

	writeJSON(w, http.StatusOK, map[string]any{"roles": roles})
}

func (h *Handler) deleteRole(w http.ResponseWriter, r *http.Request) {
	callerName := r.PathValue("name")
	role := r.PathValue("role")

	if err := h.store.UnassignRole(callerName, role); err != nil {
		writeJSON(w, http.StatusNotFound, errorBody("role not found"))
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	result := h.router.Refresh(r.Context())
	writeJSON(w, http.StatusOK, result)
}

// --- Response types ---

type callerSummary struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type callerListItem struct {
	Name      string    `json:"name"`
	KeyCount  int       `json:"key_count"`
	Roles     []string  `json:"roles"`
	CreatedAt time.Time `json:"created_at"`
}

type callerDetailResp struct {
	Name      string    `json:"name"`
	Keys      []keyItem `json:"keys"`
	Roles     []string  `json:"roles"`
	CreatedAt time.Time `json:"created_at"`
}

type keyItem struct {
	ID        int64     `json:"id"`
	Label     string    `json:"label"`
	CreatedAt time.Time `json:"created_at"`
}

type createKeyResp struct {
	Key       string    `json:"key"`
	Label     string    `json:"label"`
	CreatedAt time.Time `json:"created_at"`
}

// --- Helpers ---

func errorBody(msg string) map[string]string {
	return map[string]string{"error": msg}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

