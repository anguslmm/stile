package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/anguslmm/stile/internal/auth"
	"github.com/anguslmm/stile/internal/config"
	"github.com/anguslmm/stile/internal/jsonrpc"
	"github.com/anguslmm/stile/internal/router"
	"github.com/anguslmm/stile/internal/transport"
)

func newTestStore(t *testing.T) *auth.SQLiteStore {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := auth.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func newTestRouter(t *testing.T) *router.RouteTable {
	t.Helper()
	cfg, err := config.LoadBytes([]byte(`upstreams:
  - name: test
    transport: streamable-http
    url: http://fake
`))
	if err != nil {
		t.Fatal(err)
	}
	mock := &mockTransport{tools: []transport.ToolSchema{{Name: "test-tool"}}}
	rt, err := router.New(map[string]transport.Transport{"test": mock}, cfg.Upstreams(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rt.Close() })
	return rt
}

type mockTransport struct {
	tools []transport.ToolSchema
}

func (m *mockTransport) RoundTrip(_ context.Context, req *jsonrpc.Request) (transport.TransportResult, error) {
	if req.Method == "tools/list" {
		result := struct {
			Tools []transport.ToolSchema `json:"tools"`
		}{Tools: m.tools}
		resp, _ := jsonrpc.NewResponse(req.ID, result)
		return transport.NewJSONResult(resp), nil
	}
	resp, _ := jsonrpc.NewResponse(req.ID, json.RawMessage(`{}`))
	return transport.NewJSONResult(resp), nil
}
func (m *mockTransport) Close() error  { return nil }
func (m *mockTransport) Healthy() bool { return true }

func newTestServer(t *testing.T, store *auth.SQLiteStore, rt *router.RouteTable) *httptest.Server {
	t.Helper()
	h := NewHandler(store, rt)
	mux := http.NewServeMux()
	h.Register(mux)
	return httptest.NewServer(mux)
}

func newTestServerWithAuth(t *testing.T, store *auth.SQLiteStore, rt *router.RouteTable, adminKey string) *httptest.Server {
	t.Helper()
	h := NewHandler(store, rt)
	mux := http.NewServeMux()
	h.Register(mux)
	adminHash := sha256.Sum256([]byte(adminKey))
	adminAuth := auth.AdminAuthMiddleware(adminHash, store, false)
	return httptest.NewServer(adminAuth(mux))
}

func doRequest(t *testing.T, method, url string, body any) *http.Response {
	t.Helper()
	var bodyReader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func doAuthRequest(t *testing.T, method, url string, body any, adminKey string) *http.Response {
	t.Helper()
	var bodyReader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+adminKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func readJSON(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("unmarshal: %v (body: %s)", err, data)
	}
}

// --- Test: Create caller ---

func TestCreateCaller(t *testing.T) {
	store := newTestStore(t)
	rt := newTestRouter(t)
	ts := newTestServer(t, store, rt)
	defer ts.Close()

	resp := doRequest(t, "POST", ts.URL+"/admin/callers", map[string]string{"name": "angus"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}

	var body callerSummary
	readJSON(t, resp, &body)
	if body.Name != "angus" {
		t.Errorf("expected name=angus, got %q", body.Name)
	}
	if body.CreatedAt.IsZero() {
		t.Error("expected non-zero created_at")
	}
}

func TestCreateCallerDuplicate(t *testing.T) {
	store := newTestStore(t)
	rt := newTestRouter(t)
	ts := newTestServer(t, store, rt)
	defer ts.Close()

	doRequest(t, "POST", ts.URL+"/admin/callers", map[string]string{"name": "angus"})
	resp := doRequest(t, "POST", ts.URL+"/admin/callers", map[string]string{"name": "angus"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestCreateCallerEmptyName(t *testing.T) {
	store := newTestStore(t)
	rt := newTestRouter(t)
	ts := newTestServer(t, store, rt)
	defer ts.Close()

	resp := doRequest(t, "POST", ts.URL+"/admin/callers", map[string]string{"name": ""})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// --- Test: List callers ---

func TestListCallers(t *testing.T) {
	store := newTestStore(t)
	rt := newTestRouter(t)
	ts := newTestServer(t, store, rt)
	defer ts.Close()

	doRequest(t, "POST", ts.URL+"/admin/callers", map[string]string{"name": "alice"})
	doRequest(t, "POST", ts.URL+"/admin/callers", map[string]string{"name": "bob"})

	// Assign a role to alice and add a key.
	store.AssignRole("alice", "dev")
	doRequest(t, "POST", ts.URL+"/admin/callers/alice/keys", map[string]string{"label": "laptop"})

	resp := doRequest(t, "GET", ts.URL+"/admin/callers", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body struct {
		Callers []callerListItem `json:"callers"`
	}
	readJSON(t, resp, &body)
	if len(body.Callers) != 2 {
		t.Fatalf("expected 2 callers, got %d", len(body.Callers))
	}

	// Sorted by name.
	if body.Callers[0].Name != "alice" {
		t.Errorf("expected alice first, got %q", body.Callers[0].Name)
	}
	if body.Callers[0].KeyCount != 1 {
		t.Errorf("expected 1 key for alice, got %d", body.Callers[0].KeyCount)
	}
	if len(body.Callers[0].Roles) != 1 || body.Callers[0].Roles[0] != "dev" {
		t.Errorf("expected [dev] for alice, got %v", body.Callers[0].Roles)
	}
}

// --- Test: Get caller detail ---

func TestGetCaller(t *testing.T) {
	store := newTestStore(t)
	rt := newTestRouter(t)
	ts := newTestServer(t, store, rt)
	defer ts.Close()

	doRequest(t, "POST", ts.URL+"/admin/callers", map[string]string{"name": "angus"})
	doRequest(t, "POST", ts.URL+"/admin/callers/angus/keys", map[string]string{"label": "laptop"})
	doRequest(t, "POST", ts.URL+"/admin/callers/angus/keys", map[string]string{"label": "CI"})

	resp := doRequest(t, "GET", ts.URL+"/admin/callers/angus", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body callerDetailResp
	readJSON(t, resp, &body)
	if body.Name != "angus" {
		t.Errorf("expected angus, got %q", body.Name)
	}
	if len(body.Keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(body.Keys))
	}

	for _, k := range body.Keys {
		if k.ID == 0 {
			t.Error("expected non-zero key ID")
		}
	}
}

func TestGetCallerNotFound(t *testing.T) {
	store := newTestStore(t)
	rt := newTestRouter(t)
	ts := newTestServer(t, store, rt)
	defer ts.Close()

	resp := doRequest(t, "GET", ts.URL+"/admin/callers/nobody", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// --- Test: Delete caller ---

func TestDeleteCaller(t *testing.T) {
	store := newTestStore(t)
	rt := newTestRouter(t)
	ts := newTestServer(t, store, rt)
	defer ts.Close()

	doRequest(t, "POST", ts.URL+"/admin/callers", map[string]string{"name": "angus"})
	doRequest(t, "POST", ts.URL+"/admin/callers/angus/keys", map[string]string{"label": "laptop"})

	resp := doRequest(t, "DELETE", ts.URL+"/admin/callers/angus", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Caller should be gone.
	resp = doRequest(t, "GET", ts.URL+"/admin/callers/angus", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestDeleteCallerNotFound(t *testing.T) {
	store := newTestStore(t)
	rt := newTestRouter(t)
	ts := newTestServer(t, store, rt)
	defer ts.Close()

	resp := doRequest(t, "DELETE", ts.URL+"/admin/callers/nobody", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// --- Test: Create key ---

func TestCreateKey(t *testing.T) {
	store := newTestStore(t)
	rt := newTestRouter(t)
	ts := newTestServer(t, store, rt)
	defer ts.Close()

	doRequest(t, "POST", ts.URL+"/admin/callers", map[string]string{"name": "angus"})

	resp := doRequest(t, "POST", ts.URL+"/admin/callers/angus/keys",
		map[string]string{"label": "CI pipeline"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}

	var body createKeyResp
	readJSON(t, resp, &body)
	if body.Key == "" || body.Key[:3] != "sk-" {
		t.Errorf("expected key starting with sk-, got %q", body.Key)
	}
	if body.Label != "CI pipeline" {
		t.Errorf("expected label='CI pipeline', got %q", body.Label)
	}

	// Verify key hash is in the DB by looking up.
	hash := sha256.Sum256([]byte(body.Key))
	caller, err := store.LookupByKey(hash)
	if err != nil {
		t.Fatalf("key not found in DB: %v", err)
	}
	if caller.Name != "angus" {
		t.Errorf("expected angus, got %q", caller.Name)
	}
}

func TestCreateKeyUnknownCaller(t *testing.T) {
	store := newTestStore(t)
	rt := newTestRouter(t)
	ts := newTestServer(t, store, rt)
	defer ts.Close()

	resp := doRequest(t, "POST", ts.URL+"/admin/callers/nobody/keys",
		map[string]string{"label": "test"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// --- Test: List keys ---

func TestListKeys(t *testing.T) {
	store := newTestStore(t)
	rt := newTestRouter(t)
	ts := newTestServer(t, store, rt)
	defer ts.Close()

	doRequest(t, "POST", ts.URL+"/admin/callers", map[string]string{"name": "angus"})
	doRequest(t, "POST", ts.URL+"/admin/callers/angus/keys", map[string]string{"label": "laptop"})
	doRequest(t, "POST", ts.URL+"/admin/callers/angus/keys", map[string]string{"label": "CI"})

	resp := doRequest(t, "GET", ts.URL+"/admin/callers/angus/keys", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body struct {
		Keys []keyItem `json:"keys"`
	}
	readJSON(t, resp, &body)
	if len(body.Keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(body.Keys))
	}

	for _, k := range body.Keys {
		if k.ID == 0 {
			t.Error("expected non-zero ID")
		}
	}
}

// --- Test: Revoke key ---

func TestRevokeKey(t *testing.T) {
	store := newTestStore(t)
	rt := newTestRouter(t)
	ts := newTestServer(t, store, rt)
	defer ts.Close()

	doRequest(t, "POST", ts.URL+"/admin/callers", map[string]string{"name": "angus"})
	doRequest(t, "POST", ts.URL+"/admin/callers/angus/keys", map[string]string{"label": "laptop"})
	doRequest(t, "POST", ts.URL+"/admin/callers/angus/keys", map[string]string{"label": "CI"})

	// Get key IDs.
	resp := doRequest(t, "GET", ts.URL+"/admin/callers/angus/keys", nil)
	var keyList struct {
		Keys []keyItem `json:"keys"`
	}
	readJSON(t, resp, &keyList)

	// Delete the first key.
	keyID := keyList.Keys[0].ID
	resp = doRequest(t, "DELETE", ts.URL+"/admin/callers/angus/keys/"+strconv.FormatInt(keyID, 10), nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Verify only one key remains.
	resp = doRequest(t, "GET", ts.URL+"/admin/callers/angus/keys", nil)
	var remaining struct {
		Keys []keyItem `json:"keys"`
	}
	readJSON(t, resp, &remaining)
	if len(remaining.Keys) != 1 {
		t.Fatalf("expected 1 key after revoke, got %d", len(remaining.Keys))
	}
	if remaining.Keys[0].ID == keyID {
		t.Error("expected different key to remain")
	}
}

func TestRevokeKeyNotFound(t *testing.T) {
	store := newTestStore(t)
	rt := newTestRouter(t)
	ts := newTestServer(t, store, rt)
	defer ts.Close()

	doRequest(t, "POST", ts.URL+"/admin/callers", map[string]string{"name": "angus"})

	resp := doRequest(t, "DELETE", ts.URL+"/admin/callers/angus/keys/9999", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// --- Test: Assign role ---

func TestAssignRole(t *testing.T) {
	store := newTestStore(t)
	rt := newTestRouter(t)
	ts := newTestServer(t, store, rt)
	defer ts.Close()

	doRequest(t, "POST", ts.URL+"/admin/callers", map[string]string{"name": "angus"})

	resp := doRequest(t, "POST", ts.URL+"/admin/callers/angus/roles",
		map[string]string{"role": "dev"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body struct {
		Name  string   `json:"name"`
		Roles []string `json:"roles"`
	}
	readJSON(t, resp, &body)
	if body.Name != "angus" {
		t.Errorf("expected name=angus, got %q", body.Name)
	}
	if len(body.Roles) != 1 || body.Roles[0] != "dev" {
		t.Errorf("expected [dev], got %v", body.Roles)
	}
}

func TestAssignRoleIdempotent(t *testing.T) {
	store := newTestStore(t)
	rt := newTestRouter(t)
	ts := newTestServer(t, store, rt)
	defer ts.Close()

	doRequest(t, "POST", ts.URL+"/admin/callers", map[string]string{"name": "angus"})
	doRequest(t, "POST", ts.URL+"/admin/callers/angus/roles", map[string]string{"role": "dev"})

	resp := doRequest(t, "POST", ts.URL+"/admin/callers/angus/roles",
		map[string]string{"role": "dev"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body struct {
		Roles []string `json:"roles"`
	}
	readJSON(t, resp, &body)
	if len(body.Roles) != 1 {
		t.Errorf("expected 1 role (no duplicate), got %v", body.Roles)
	}
}

func TestAssignRoleUnknownCaller(t *testing.T) {
	store := newTestStore(t)
	rt := newTestRouter(t)
	ts := newTestServer(t, store, rt)
	defer ts.Close()

	resp := doRequest(t, "POST", ts.URL+"/admin/callers/nobody/roles",
		map[string]string{"role": "dev"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestAssignRoleEmpty(t *testing.T) {
	store := newTestStore(t)
	rt := newTestRouter(t)
	ts := newTestServer(t, store, rt)
	defer ts.Close()

	doRequest(t, "POST", ts.URL+"/admin/callers", map[string]string{"name": "angus"})

	resp := doRequest(t, "POST", ts.URL+"/admin/callers/angus/roles",
		map[string]string{"role": ""})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// --- Test: Unassign role ---

func TestUnassignRole(t *testing.T) {
	store := newTestStore(t)
	rt := newTestRouter(t)
	ts := newTestServer(t, store, rt)
	defer ts.Close()

	doRequest(t, "POST", ts.URL+"/admin/callers", map[string]string{"name": "angus"})
	doRequest(t, "POST", ts.URL+"/admin/callers/angus/roles", map[string]string{"role": "dev"})

	resp := doRequest(t, "DELETE", ts.URL+"/admin/callers/angus/roles/dev", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Verify role is gone.
	resp = doRequest(t, "GET", ts.URL+"/admin/callers/angus/roles", nil)
	var body struct {
		Roles []string `json:"roles"`
	}
	readJSON(t, resp, &body)
	if len(body.Roles) != 0 {
		t.Errorf("expected no roles after unassign, got %v", body.Roles)
	}
}

func TestUnassignRoleNotAssigned(t *testing.T) {
	store := newTestStore(t)
	rt := newTestRouter(t)
	ts := newTestServer(t, store, rt)
	defer ts.Close()

	doRequest(t, "POST", ts.URL+"/admin/callers", map[string]string{"name": "angus"})

	resp := doRequest(t, "DELETE", ts.URL+"/admin/callers/angus/roles/nonexistent", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// --- Test: List roles ---

func TestListRoles(t *testing.T) {
	store := newTestStore(t)
	rt := newTestRouter(t)
	ts := newTestServer(t, store, rt)
	defer ts.Close()

	doRequest(t, "POST", ts.URL+"/admin/callers", map[string]string{"name": "angus"})
	doRequest(t, "POST", ts.URL+"/admin/callers/angus/roles", map[string]string{"role": "dev"})
	doRequest(t, "POST", ts.URL+"/admin/callers/angus/roles", map[string]string{"role": "prod"})

	resp := doRequest(t, "GET", ts.URL+"/admin/callers/angus/roles", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body struct {
		Roles []string `json:"roles"`
	}
	readJSON(t, resp, &body)
	if len(body.Roles) != 2 {
		t.Fatalf("expected 2 roles, got %v", body.Roles)
	}
}

func TestListRolesUnknownCaller(t *testing.T) {
	store := newTestStore(t)
	rt := newTestRouter(t)
	ts := newTestServer(t, store, rt)
	defer ts.Close()

	resp := doRequest(t, "GET", ts.URL+"/admin/callers/nobody/roles", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// --- Test: Caller detail includes roles ---

func TestGetCallerIncludesRoles(t *testing.T) {
	store := newTestStore(t)
	rt := newTestRouter(t)
	ts := newTestServer(t, store, rt)
	defer ts.Close()

	doRequest(t, "POST", ts.URL+"/admin/callers", map[string]string{"name": "angus"})
	doRequest(t, "POST", ts.URL+"/admin/callers/angus/roles", map[string]string{"role": "dev"})
	doRequest(t, "POST", ts.URL+"/admin/callers/angus/roles", map[string]string{"role": "prod"})

	resp := doRequest(t, "GET", ts.URL+"/admin/callers/angus", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body callerDetailResp
	readJSON(t, resp, &body)
	if len(body.Roles) != 2 {
		t.Fatalf("expected 2 roles in caller detail, got %v", body.Roles)
	}
}

// --- Test: Admin auth required ---

func TestAdminAuthRequired(t *testing.T) {
	store := newTestStore(t)
	rt := newTestRouter(t)
	adminKey := "admin-secret"

	// Add a caller so dev mode doesn't kick in.
	store.AddCaller("existing")

	ts := newTestServerWithAuth(t, store, rt, adminKey)
	defer ts.Close()

	// Without auth header — should get 403.
	resp := doRequest(t, "GET", ts.URL+"/admin/callers", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 without admin key, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// With wrong key — should get 403.
	resp = doAuthRequest(t, "GET", ts.URL+"/admin/callers", nil, "wrong-key")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 with wrong key, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// With correct key — should get 200.
	resp = doAuthRequest(t, "GET", ts.URL+"/admin/callers", nil, adminKey)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with valid admin key, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// --- Test: Refresh backward compat ---

func TestRefreshEndpoint(t *testing.T) {
	store := newTestStore(t)
	rt := newTestRouter(t)
	ts := newTestServer(t, store, rt)
	defer ts.Close()

	resp := doRequest(t, "POST", ts.URL+"/admin/refresh", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body router.RefreshResult
	readJSON(t, resp, &body)
	if _, ok := body.Upstreams["test"]; !ok {
		t.Error("expected 'test' upstream in refresh result")
	}
}

