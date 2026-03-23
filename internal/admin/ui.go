package admin

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/anguslmm/stile/internal/audit"
	"github.com/anguslmm/stile/internal/auth"
)

//go:embed ui/templates/*.html
var templateFS embed.FS

// pageData is the base data passed to all page templates.
type pageData struct {
	Title string
	Nav   string
}

// registerUI registers the embedded web UI routes on the given mux.
// Login/logout are exempted from admin auth by the middleware.
// All other UI routes require either a Bearer token or a valid session cookie
// (enforced by the admin auth middleware, which redirects to login for UI paths).
func (h *Handler) registerUI(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/ui/login", h.uiLoginPage)
	mux.HandleFunc("POST /admin/ui/login", h.uiLoginSubmit)
	mux.HandleFunc("POST /admin/ui/logout", h.uiLogout)

	mux.HandleFunc("GET /admin/ui/", h.uiDashboard)
	mux.HandleFunc("GET /admin/ui/callers", h.uiCallers)
	mux.HandleFunc("POST /admin/ui/callers", h.uiCreateCaller)
	mux.HandleFunc("GET /admin/ui/callers/{name}", h.uiCallerDetail)
	mux.HandleFunc("POST /admin/ui/callers/{name}/delete", h.uiDeleteCaller)
	mux.HandleFunc("POST /admin/ui/callers/{name}/keys", h.uiCreateKey)
	mux.HandleFunc("POST /admin/ui/callers/{name}/keys/{id}/revoke", h.uiRevokeKey)
	mux.HandleFunc("POST /admin/ui/callers/{name}/roles", h.uiAssignRole)
	mux.HandleFunc("POST /admin/ui/callers/{name}/roles/{role}/unassign", h.uiUnassignRole)
	mux.HandleFunc("GET /admin/ui/config", h.uiConfig)
	mux.HandleFunc("GET /admin/ui/audit", h.uiAudit)
}

func (h *Handler) uiLoginPage(w http.ResponseWriter, _ *http.Request) {
	renderTemplate(w, "login", struct{ Error string }{})
}

func (h *Handler) uiLoginSubmit(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	key := r.FormValue("key")

	hash := sha256.Sum256([]byte(key))
	if subtle.ConstantTimeCompare(hash[:], h.adminKeyHash[:]) != 1 {
		renderTemplate(w, "login", struct{ Error string }{Error: "Invalid admin key."})
		return
	}

	setSessionCookie(w, signSession(h.adminKeyHash))
	http.Redirect(w, r, "/admin/ui/", http.StatusFound)
}

func (h *Handler) uiLogout(w http.ResponseWriter, r *http.Request) {
	clearSessionCookie(w)
	http.Redirect(w, r, "/admin/ui/login", http.StatusFound)
}

func (h *Handler) uiDashboard(w http.ResponseWriter, _ *http.Request) {
	status := h.buildStatus()
	uptime := formatUptime(time.Since(h.startTime))

	data := struct {
		pageData
		Status statusResponse
		Uptime string
	}{
		pageData: pageData{Title: "Dashboard", Nav: "dashboard"},
		Status:   status,
		Uptime:   uptime,
	}
	renderPage(w, "dashboard.html", data)
}

func (h *Handler) buildStatus() statusResponse {
	status := statusResponse{
		UptimeSeconds: int64(time.Since(h.startTime).Seconds()),
	}
	if h.healthChecker != nil {
		statuses := h.healthChecker.UpstreamStatuses()
		status.Upstreams = make([]upstreamStatusItem, 0, len(statuses))
		for name, us := range statuses {
			status.Upstreams = append(status.Upstreams, upstreamStatusItem{
				Name:        name,
				Healthy:     us.Healthy,
				ToolsCached: us.Tools,
				Stale:       us.Stale,
			})
		}
	}
	callers, err := h.store.ListCallers()
	if err == nil {
		status.CallersCount = len(callers)
	}
	return status
}

func (h *Handler) uiCallers(w http.ResponseWriter, r *http.Request) {
	callers, _ := h.store.ListCallers()
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

	data := struct {
		pageData
		Callers []callerListItem
		Flash   string
	}{
		pageData: pageData{Title: "Callers", Nav: "callers"},
		Callers:  items,
		Flash:    consumeFlash(r, w),
	}
	renderPage(w, "callers.html", data)
}

func (h *Handler) uiCreateCaller(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	name := r.FormValue("name")
	if name == "" {
		setFlash(w, "Name is required")
		http.Redirect(w, r, "/admin/ui/callers", http.StatusFound)
		return
	}
	if err := h.store.AddCaller(name); err != nil {
		setFlash(w, "Could not create caller: "+err.Error())
	}
	http.Redirect(w, r, "/admin/ui/callers", http.StatusFound)
}

func (h *Handler) uiDeleteCaller(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	h.store.DeleteCaller(name)
	http.Redirect(w, r, "/admin/ui/callers", http.StatusFound)
}

func (h *Handler) uiCallerDetail(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	detail, err := h.store.GetCaller(name)
	if err != nil {
		http.Redirect(w, r, "/admin/ui/callers", http.StatusFound)
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
	roles, _ := h.store.RolesForCaller(name)
	if roles == nil {
		roles = []string{}
	}

	// Get available roles from config for dropdown.
	var availableRoles []string
	for _, rv := range h.configView.Roles {
		availableRoles = append(availableRoles, rv.Name)
	}

	data := struct {
		pageData
		Caller         callerDetailResp
		AvailableRoles []string
		NewKey         string
	}{
		pageData: pageData{Title: "Caller: " + name, Nav: "callers"},
		Caller: callerDetailResp{
			Name:      detail.Name,
			Keys:      keys,
			Roles:     roles,
			CreatedAt: detail.CreatedAt,
		},
		AvailableRoles: availableRoles,
		NewKey:         consumeFlash(r, w),
	}
	renderPage(w, "caller_detail.html", data)
}

func (h *Handler) uiCreateKey(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	r.ParseForm()
	label := r.FormValue("label")

	rawKey, err := auth.GenerateAPIKey()
	if err != nil {
		http.Redirect(w, r, "/admin/ui/callers/"+name, http.StatusFound)
		return
	}
	hash := sha256.Sum256([]byte(rawKey))
	if err := h.store.AddKey(name, hash, label); err != nil {
		http.Redirect(w, r, "/admin/ui/callers/"+name, http.StatusFound)
		return
	}

	// Flash the new key so it's shown once after the redirect.
	setFlash(w, rawKey)
	http.Redirect(w, r, "/admin/ui/callers/"+name, http.StatusFound)
}

func (h *Handler) uiRevokeKey(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	idStr := r.PathValue("id")
	if id, err := strconv.ParseInt(idStr, 10, 64); err == nil {
		h.store.DeleteKey(name, id)
	}
	http.Redirect(w, r, "/admin/ui/callers/"+name, http.StatusFound)
}

func (h *Handler) uiAssignRole(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	r.ParseForm()
	role := r.FormValue("role")
	if role != "" {
		h.store.AssignRole(name, role)
	}
	http.Redirect(w, r, "/admin/ui/callers/"+name, http.StatusFound)
}

func (h *Handler) uiUnassignRole(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	role := r.PathValue("role")
	h.store.UnassignRole(name, role)
	http.Redirect(w, r, "/admin/ui/callers/"+name, http.StatusFound)
}

func (h *Handler) uiConfig(w http.ResponseWriter, _ *http.Request) {
	configJSON, _ := json.MarshalIndent(h.configView, "", "  ")
	data := struct {
		pageData
		ConfigJSON string
	}{
		pageData:   pageData{Title: "Configuration", Nav: "config"},
		ConfigJSON: string(configJSON),
	}
	renderPage(w, "config.html", data)
}

// auditUIEntry extends auditEntryItem with formatted params for the template.
type auditUIEntry struct {
	auditEntryItem
	ParamsFormatted string
}

func (h *Handler) uiAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := audit.QueryFilter{
		Caller:   q.Get("caller"),
		Tool:     q.Get("tool"),
		Upstream: q.Get("upstream"),
		Status:   q.Get("status"),
	}
	limit := 50
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	filter.Limit = limit + 1 // fetch one extra to detect "has more"
	offset := 0
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	filter.Offset = offset

	hasFilters := filter.Caller != "" || filter.Tool != "" || filter.Upstream != "" || filter.Status != ""

	var entries []auditUIEntry
	hasMore := false
	auditEnabled := h.auditReader != nil

	if auditEnabled {
		results, err := h.auditReader.Query(r.Context(), filter)
		if err == nil {
			if len(results) > limit {
				hasMore = true
				results = results[:limit]
			}
			for _, e := range results {
				ue := auditUIEntry{
					auditEntryItem: auditEntryItem{
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
					},
				}
				if e.Params != nil {
					var buf bytes.Buffer
					json.Indent(&buf, e.Params, "", "  ")
					ue.ParamsFormatted = buf.String()
				}
				entries = append(entries, ue)
			}
		}
	}

	// Build pagination query strings.
	buildQuery := func(newOffset int) string {
		v := url.Values{}
		if filter.Caller != "" {
			v.Set("caller", filter.Caller)
		}
		if filter.Tool != "" {
			v.Set("tool", filter.Tool)
		}
		if filter.Upstream != "" {
			v.Set("upstream", filter.Upstream)
		}
		if filter.Status != "" {
			v.Set("status", filter.Status)
		}
		if newOffset > 0 {
			v.Set("offset", strconv.Itoa(newOffset))
		}
		return v.Encode()
	}

	prevOffset := offset - limit
	if prevOffset < 0 {
		prevOffset = 0
	}

	data := struct {
		pageData
		Entries      []auditUIEntry
		Filter       audit.QueryFilter
		HasFilters   bool
		AuditEnabled bool
		HasMore      bool
		Offset       int
		Limit        int
		PrevQuery    string
		NextQuery    string
	}{
		pageData:     pageData{Title: "Audit Log", Nav: "audit"},
		Entries:      entries,
		Filter:       filter,
		HasFilters:   hasFilters,
		AuditEnabled: auditEnabled,
		HasMore:      hasMore,
		Offset:       offset,
		Limit:        limit,
		PrevQuery:    buildQuery(prevOffset),
		NextQuery:    buildQuery(offset + limit),
	}
	renderPage(w, "audit.html", data)
}

// renderPage parses layout.html + the named page template and executes them.
// Each page defines its own {{define "content"}} block; parsing per-call avoids
// the "last define wins" problem when all pages are in one template set.
func renderPage(w http.ResponseWriter, page string, data any) {
	t, err := template.ParseFS(templateFS, "ui/templates/layout.html", "ui/templates/"+page)
	if err != nil {
		http.Error(w, fmt.Sprintf("template parse error: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "layout", data); err != nil {
		http.Error(w, fmt.Sprintf("template error: %v", err), http.StatusInternalServerError)
	}
}

// renderTemplate renders a standalone template (e.g. login page, no layout).
func renderTemplate(w http.ResponseWriter, name string, data any) {
	t, err := template.ParseFS(templateFS, "ui/templates/"+name+".html")
	if err != nil {
		http.Error(w, fmt.Sprintf("template parse error: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, fmt.Sprintf("template error: %v", err), http.StatusInternalServerError)
	}
}

const flashCookieName = "stile_flash"

func setFlash(w http.ResponseWriter, msg string) {
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookieName,
		Value:    url.QueryEscape(msg),
		Path:     "/admin/ui/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   10,
	})
}

func consumeFlash(r *http.Request, w http.ResponseWriter) string {
	cookie, err := r.Cookie(flashCookieName)
	if err != nil {
		return ""
	}
	// Clear the flash cookie.
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookieName,
		Value:    "",
		Path:     "/admin/ui/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
	val, _ := url.QueryUnescape(cookie.Value)
	return val
}

func formatUptime(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60

	if days > 0 {
		return fmt.Sprintf("%dd %dh", days, hours)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, mins)
	}
	return fmt.Sprintf("%dm", mins)
}
