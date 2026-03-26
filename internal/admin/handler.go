// Package admin implements the HTTP admin API for managing callers and API keys.
package admin

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/anguslmm/stile/internal/audit"
	"github.com/anguslmm/stile/internal/auth"
	"github.com/anguslmm/stile/internal/config"
	"github.com/anguslmm/stile/internal/health"
	"github.com/anguslmm/stile/internal/router"
)

// Option configures optional Handler behavior.
type Option func(*Handler)

// WithHealthChecker adds upstream health data to the status endpoint.
func WithHealthChecker(hc *health.Checker) Option {
	return func(h *Handler) { h.healthChecker = hc }
}

// WithConfig enables the /admin/config endpoint with a sanitized config view.
func WithConfig(cfg *config.Config) Option {
	return func(h *Handler) { h.configView = NewConfigView(cfg) }
}

// WithStartTime sets the gateway start time for uptime reporting.
func WithStartTime(t time.Time) Option {
	return func(h *Handler) { h.startTime = t }
}

// WithAuditReader enables the /admin/audit query endpoint.
func WithAuditReader(r audit.Reader) Option {
	return func(h *Handler) { h.auditReader = r }
}

// WithAdminKeyHash sets the admin key hash for session-based UI login.
func WithAdminKeyHash(hash [32]byte) Option {
	return func(h *Handler) { h.adminKeyHash = hash }
}

// WithTokenStore enables the connections endpoints for managing OAuth tokens.
func WithTokenStore(ts auth.TokenStore) Option {
	return func(h *Handler) { h.tokenStore = ts }
}

// WithOAuthProviders sets the configured OAuth provider names for the connections UI.
func WithOAuthProviders(names []string) Option {
	return func(h *Handler) { h.oauthProviders = names }
}

// Handler serves admin API endpoints for caller and key management.
type Handler struct {
	store          auth.Store
	router         *router.RouteTable
	healthChecker  *health.Checker
	configView     ConfigView
	startTime      time.Time
	auditReader    audit.Reader
	adminKeyHash   [32]byte
	tokenStore     auth.TokenStore
	oauthProviders []string
}

// SessionCheck validates session cookies on incoming requests.
// Pass this to auth.WithSessionCheck to allow cookie-based admin UI access.
func (h *Handler) SessionCheck(r *http.Request) bool {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return false
	}
	return verifySession(cookie.Value, h.adminKeyHash)
}

// NewHandler creates an admin handler.
func NewHandler(store auth.Store, rt *router.RouteTable, opts ...Option) *Handler {
	h := &Handler{store: store, router: rt, startTime: time.Now()}
	for _, opt := range opts {
		opt(h)
	}
	return h
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
	mux.HandleFunc("GET /admin/cache", h.cacheStats)
	mux.HandleFunc("DELETE /admin/cache", h.cacheFlush)
	mux.HandleFunc("GET /admin/config", h.getConfig)
	mux.HandleFunc("GET /admin/status", h.getStatus)
	mux.HandleFunc("GET /admin/audit", h.queryAudit)
	mux.HandleFunc("GET /admin/connections", h.listConnections)
	mux.HandleFunc("DELETE /admin/connections/{provider}", h.deleteConnection)
	mux.HandleFunc("PUT /admin/connections/{provider}", h.putConnection)
	h.registerUI(mux)
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

func (h *Handler) cacheStats(w http.ResponseWriter, _ *http.Request) {
	if c, ok := h.store.(auth.Cacheable); ok {
		writeJSON(w, http.StatusOK, c.Stats())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cache not enabled"})
}

func (h *Handler) cacheFlush(w http.ResponseWriter, _ *http.Request) {
	if c, ok := h.store.(auth.Cacheable); ok {
		c.Flush()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cache not enabled"})
}

func (h *Handler) queryAudit(w http.ResponseWriter, r *http.Request) {
	if h.auditReader == nil {
		writeJSON(w, http.StatusOK, map[string]any{"entries": []any{}, "message": "audit not enabled"})
		return
	}

	q := r.URL.Query()
	filter := audit.QueryFilter{
		Caller:   q.Get("caller"),
		Tool:     q.Get("tool"),
		Upstream: q.Get("upstream"),
		Status:   q.Get("status"),
	}

	if v := q.Get("start"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.Start = t
		}
	}
	if v := q.Get("end"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.End = t
		}
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			filter.Limit = n
		}
	}
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			filter.Offset = n
		}
	}

	entries, err := h.auditReader.Query(r.Context(), filter)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("audit query failed"))
		return
	}

	items := make([]auditEntryItem, len(entries))
	for i, e := range entries {
		items[i] = auditEntryItem{
			ID:        e.ID,
			Timestamp: e.Timestamp,
			Caller:    e.Caller,
			Method:    e.Method,
			Tool:      e.Tool,
			Upstream:  e.Upstream,
			Params:    e.Params,
			Status:    e.Status,
			LatencyMS: e.LatencyMS,
			TraceID:   e.TraceID,
			KeyLabel:  e.KeyLabel,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": items})
}

func (h *Handler) getConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.configView)
}

func (h *Handler) getStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.buildStatus())
}

// --- Connection endpoints ---

func (h *Handler) listConnections(w http.ResponseWriter, r *http.Request) {
	if h.tokenStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"connections": []any{},
			"message":     "OAuth not configured",
		})
		return
	}

	caller := r.URL.Query().Get("caller")
	if caller == "" {
		caller = r.URL.Query().Get("user")
	}

	// Build the list of all providers and their connection status.
	type connectionItem struct {
		Provider  string     `json:"provider"`
		Connected bool       `json:"connected"`
		Expiry    *time.Time `json:"expiry,omitempty"`
		Scopes    string     `json:"scopes,omitempty"`
		Expired   bool       `json:"expired,omitempty"`
	}

	var items []connectionItem
	for _, prov := range h.oauthProviders {
		item := connectionItem{Provider: prov}
		if caller != "" {
			token, err := h.tokenStore.GetToken(context.Background(), caller, prov)
			if err == nil {
				item.Connected = true
				if !token.Expiry.IsZero() {
					item.Expiry = &token.Expiry
					item.Expired = token.Expired()
				}
				item.Scopes = token.Scopes
			}
		}
		items = append(items, item)
	}
	if items == nil {
		items = []connectionItem{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"connections": items})
}

func (h *Handler) deleteConnection(w http.ResponseWriter, r *http.Request) {
	if h.tokenStore == nil {
		writeJSON(w, http.StatusNotFound, errorBody("OAuth not configured"))
		return
	}

	provider := r.PathValue("provider")
	caller := r.URL.Query().Get("caller")
	if caller == "" {
		caller = r.URL.Query().Get("user")
	}
	if caller == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("caller query parameter is required"))
		return
	}

	if err := h.tokenStore.DeleteToken(context.Background(), caller, provider); err != nil {
		if errors.Is(err, auth.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, errorBody("connection not found"))
			return
		}
		writeJSON(w, http.StatusInternalServerError, errorBody("internal error"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) putConnection(w http.ResponseWriter, r *http.Request) {
	if h.tokenStore == nil {
		writeJSON(w, http.StatusNotFound, errorBody("OAuth not configured"))
		return
	}

	provider := r.PathValue("provider")

	var req struct {
		Caller       string `json:"caller"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid JSON"))
		return
	}
	if req.Caller == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("caller is required"))
		return
	}
	if req.AccessToken == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("access_token is required"))
		return
	}

	token := &auth.OAuthToken{
		AccessToken:  req.AccessToken,
		RefreshToken: req.RefreshToken,
		TokenType:    "Bearer",
	}

	if err := h.tokenStore.StoreToken(context.Background(), req.Caller, provider, token); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("internal error"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status":   "ok",
		"caller":   req.Caller,
		"provider": provider,
	})
}

// --- Response types ---

type statusResponse struct {
	Upstreams     []upstreamStatusItem `json:"upstreams"`
	CallersCount  int                  `json:"callers_count"`
	UptimeSeconds int64                `json:"uptime_seconds"`
}

type upstreamStatusItem struct {
	Name        string `json:"name"`
	Healthy     bool   `json:"healthy"`
	ToolsCached int    `json:"tools_cached"`
	Stale       bool   `json:"stale"`
}

type auditEntryItem struct {
	ID        int64           `json:"id"`
	Timestamp time.Time       `json:"timestamp"`
	Caller    string          `json:"caller"`
	Method    string          `json:"method"`
	Tool      string          `json:"tool,omitempty"`
	Upstream  string          `json:"upstream,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Status    string          `json:"status"`
	LatencyMS int64           `json:"latency_ms"`
	TraceID   string          `json:"trace_id,omitempty"`
	KeyLabel  string          `json:"key_label,omitempty"`
}

// --- Caller response types ---

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

