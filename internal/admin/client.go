package admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/anguslmm/stile/internal/auth"
)

// Client is a thin HTTP client for the Stile admin API, providing
// the same caller-management operations as the local store.
type Client struct {
	baseURL    string
	adminKey   string
	httpClient *http.Client
}

// NewClient creates a new admin API client targeting the given base URL.
// The adminKey is sent as a Bearer token on every request.
func NewClient(baseURL, adminKey string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		adminKey:   adminKey,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// AddCaller creates a new caller via the admin API.
func (c *Client) AddCaller(name string) error {
	resp, err := c.do("POST", "/admin/callers", map[string]string{"name": name})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return c.readError(resp)
	}
	return nil
}

// ListCallers returns all callers from the admin API.
func (c *Client) ListCallers() ([]auth.CallerInfo, error) {
	resp, err := c.do("GET", "/admin/callers", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, c.readError(resp)
	}

	var body struct {
		Callers []callerListItem `json:"callers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	callers := make([]auth.CallerInfo, len(body.Callers))
	for i, item := range body.Callers {
		callers[i] = auth.CallerInfo{
			Name:      item.Name,
			KeyCount:  item.KeyCount,
			Roles:     item.Roles,
			CreatedAt: item.CreatedAt,
		}
	}
	return callers, nil
}

// DeleteCaller removes a caller via the admin API.
func (c *Client) DeleteCaller(name string) error {
	resp, err := c.do("DELETE", "/admin/callers/"+name, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return c.readError(resp)
	}
	return nil
}

// KeyCountForCaller returns the number of keys a caller has.
func (c *Client) KeyCountForCaller(name string) (int, error) {
	keys, err := c.ListKeys(name)
	if err != nil {
		return 0, err
	}
	return len(keys), nil
}

// CreateKey creates a new API key for a caller and returns the raw key.
func (c *Client) CreateKey(callerName, label string) (string, error) {
	resp, err := c.do("POST", "/admin/callers/"+callerName+"/keys", map[string]string{"label": label})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return "", c.readError(resp)
	}
	var body createKeyResp
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	return body.Key, nil
}

// ListKeys returns metadata for all keys belonging to a caller.
func (c *Client) ListKeys(callerName string) ([]auth.KeyInfo, error) {
	resp, err := c.do("GET", "/admin/callers/"+callerName+"/keys", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, c.readError(resp)
	}

	var body struct {
		Keys []keyItem `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	keys := make([]auth.KeyInfo, len(body.Keys))
	for i, item := range body.Keys {
		keys[i] = auth.KeyInfo{
			ID:        item.ID,
			Label:     item.Label,
			CreatedAt: item.CreatedAt,
		}
	}
	return keys, nil
}

// RevokeKey revokes a key by label. It lists keys to resolve the label
// to an ID, then deletes by ID.
func (c *Client) RevokeKey(callerName, label string) error {
	keys, err := c.ListKeys(callerName)
	if err != nil {
		return err
	}
	for _, k := range keys {
		if k.Label == label {
			resp, err := c.do("DELETE", "/admin/callers/"+callerName+"/keys/"+strconv.FormatInt(k.ID, 10), nil)
			if err != nil {
				return err
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusNoContent {
				return fmt.Errorf("remote error: HTTP %d", resp.StatusCode)
			}
			return nil
		}
	}
	return fmt.Errorf("no key with label %q found for caller %q", label, callerName)
}

// AssignRole assigns a role to a caller via the admin API.
func (c *Client) AssignRole(callerName, role string) error {
	resp, err := c.do("POST", "/admin/callers/"+callerName+"/roles", map[string]string{"role": role})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return c.readError(resp)
	}
	return nil
}

// UnassignRole removes a role from a caller via the admin API.
func (c *Client) UnassignRole(callerName, role string) error {
	resp, err := c.do("DELETE", "/admin/callers/"+callerName+"/roles/"+role, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return c.readError(resp)
	}
	return nil
}

// CacheStats retrieves cache statistics from the remote admin API.
func (c *Client) CacheStats() (*auth.CacheStats, error) {
	resp, err := c.do("GET", "/admin/cache", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, c.readError(resp)
	}
	var stats auth.CacheStats
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &stats, nil
}

// CacheFlush flushes the auth cache on the remote server.
func (c *Client) CacheFlush() error {
	resp, err := c.do("DELETE", "/admin/cache", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return c.readError(resp)
	}
	return nil
}

// Close is a no-op for the HTTP client.
func (c *Client) Close() error { return nil }

func (c *Client) do(method, path string, body any) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, c.baseURL+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.adminKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach remote at %s: %w", c.baseURL, err)
	}
	return resp, nil
}

func (c *Client) readError(resp *http.Response) error {
	var body struct {
		Error string `json:"error"`
	}
	data, _ := io.ReadAll(resp.Body)
	if json.Unmarshal(data, &body) == nil && body.Error != "" {
		return fmt.Errorf("remote error: %s", body.Error)
	}
	return fmt.Errorf("remote error: HTTP %d", resp.StatusCode)
}
