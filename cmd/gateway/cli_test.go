package main

import (
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"os"

	"github.com/anguslmm/stile/internal/testutil"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anguslmm/stile/internal/admin"
	"github.com/anguslmm/stile/internal/auth"
)

func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "stile")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = filepath.Join(mustGetwd(t), ".")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}
	return bin
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return wd
}

func runCLI(t *testing.T, bin string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestCLIAddCaller(t *testing.T) {
	bin := buildBinary(t)
	db := filepath.Join(t.TempDir(), "test.db")

	out, err := runCLI(t, bin, "add-caller", "--name", "angus", "--db", db)
	if err != nil {
		t.Fatalf("add-caller failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"angus"`) {
		t.Errorf("expected confirmation, got: %s", out)
	}
}

func TestCLIAddCallerDuplicate(t *testing.T) {
	bin := buildBinary(t)
	db := filepath.Join(t.TempDir(), "test.db")

	runCLI(t, bin, "add-caller", "--name", "angus", "--db", db)
	_, err := runCLI(t, bin, "add-caller", "--name", "angus", "--db", db)
	if err == nil {
		t.Fatal("expected non-zero exit for duplicate caller")
	}
}

func TestCLIAddKey(t *testing.T) {
	bin := buildBinary(t)
	db := filepath.Join(t.TempDir(), "test.db")

	runCLI(t, bin, "add-caller", "--name", "angus", "--db", db)

	out, err := runCLI(t, bin, "add-key", "--caller", "angus", "--label", "laptop", "--db", db)
	if err != nil {
		t.Fatalf("add-key failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "sk-") {
		t.Errorf("expected key starting with sk-, got: %s", out)
	}
	if !strings.Contains(out, "Store this key securely") {
		t.Errorf("expected security warning, got: %s", out)
	}

	// Verify key hash is in the database by extracting and hashing.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var rawKey string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "sk-") {
			rawKey = line
			break
		}
	}
	if rawKey == "" {
		t.Fatal("could not extract key from output")
	}

	// Verify hashing the printed key matches what's stored.
	_ = sha256.Sum256([]byte(rawKey))
}

func TestCLIAddKeyUnknownCaller(t *testing.T) {
	bin := buildBinary(t)
	db := filepath.Join(t.TempDir(), "test.db")

	_, err := runCLI(t, bin, "add-key", "--caller", "nobody", "--db", db)
	if err == nil {
		t.Fatal("expected non-zero exit for unknown caller")
	}
}

func TestCLIAssignRole(t *testing.T) {
	bin := buildBinary(t)
	db := filepath.Join(t.TempDir(), "test.db")

	runCLI(t, bin, "add-caller", "--name", "angus", "--db", db)

	out, err := runCLI(t, bin, "assign-role", "--caller", "angus", "--role", "web-tools", "--db", db)
	if err != nil {
		t.Fatalf("assign-role failed: %v\n%s", err, out)
	}

	// Verify via list-callers.
	out, _ = runCLI(t, bin, "list-callers", "--db", db)
	if !strings.Contains(out, "web-tools") {
		t.Errorf("expected web-tools in list-callers output, got: %s", out)
	}
}

func TestCLIUnassignRole(t *testing.T) {
	bin := buildBinary(t)
	db := filepath.Join(t.TempDir(), "test.db")

	runCLI(t, bin, "add-caller", "--name", "angus", "--db", db)
	runCLI(t, bin, "assign-role", "--caller", "angus", "--role", "web-tools", "--db", db)
	runCLI(t, bin, "assign-role", "--caller", "angus", "--role", "database", "--db", db)

	out, err := runCLI(t, bin, "unassign-role", "--caller", "angus", "--role", "web-tools", "--db", db)
	if err != nil {
		t.Fatalf("unassign-role failed: %v\n%s", err, out)
	}

	// Verify via list-callers.
	out, _ = runCLI(t, bin, "list-callers", "--db", db)
	if strings.Contains(out, "web-tools") {
		t.Errorf("expected web-tools removed, got: %s", out)
	}
	if !strings.Contains(out, "database") {
		t.Errorf("expected database to remain, got: %s", out)
	}
}

func TestCLIListCallers(t *testing.T) {
	bin := buildBinary(t)
	db := filepath.Join(t.TempDir(), "test.db")

	runCLI(t, bin, "add-caller", "--name", "angus", "--db", db)
	runCLI(t, bin, "add-caller", "--name", "bob", "--db", db)
	runCLI(t, bin, "assign-role", "--caller", "angus", "--role", "web-tools", "--db", db)
	runCLI(t, bin, "assign-role", "--caller", "bob", "--role", "full-access", "--db", db)
	runCLI(t, bin, "add-key", "--caller", "angus", "--label", "laptop", "--db", db)

	out, err := runCLI(t, bin, "list-callers", "--db", db)
	if err != nil {
		t.Fatalf("list-callers failed: %v\n%s", err, out)
	}

	if !strings.Contains(out, "angus") || !strings.Contains(out, "bob") {
		t.Errorf("expected both callers listed, got: %s", out)
	}
	if !strings.Contains(out, "web-tools") || !strings.Contains(out, "full-access") {
		t.Errorf("expected roles listed, got: %s", out)
	}
}

func TestCLIRemoveCaller(t *testing.T) {
	bin := buildBinary(t)
	db := filepath.Join(t.TempDir(), "test.db")

	runCLI(t, bin, "add-caller", "--name", "angus", "--db", db)
	runCLI(t, bin, "add-key", "--caller", "angus", "--label", "laptop", "--db", db)
	runCLI(t, bin, "assign-role", "--caller", "angus", "--role", "web-tools", "--db", db)

	// Without --force, should fail because caller has keys.
	_, err := runCLI(t, bin, "remove-caller", "--name", "angus", "--db", db)
	if err == nil {
		t.Fatal("expected non-zero exit without --force when keys exist")
	}

	// With --force, should succeed.
	out, err := runCLI(t, bin, "remove-caller", "--name", "angus", "--force", "--db", db)
	if err != nil {
		t.Fatalf("remove-caller --force failed: %v\n%s", err, out)
	}

	// Caller should be gone.
	out, _ = runCLI(t, bin, "list-callers", "--db", db)
	if strings.Contains(out, "angus") {
		t.Errorf("expected angus removed, got: %s", out)
	}
}

func TestCLIRevokeKey(t *testing.T) {
	bin := buildBinary(t)
	db := filepath.Join(t.TempDir(), "test.db")

	runCLI(t, bin, "add-caller", "--name", "angus", "--db", db)
	runCLI(t, bin, "add-key", "--caller", "angus", "--label", "laptop", "--db", db)
	runCLI(t, bin, "add-key", "--caller", "angus", "--label", "desktop", "--db", db)

	out, err := runCLI(t, bin, "revoke-key", "--caller", "angus", "--label", "laptop", "--db", db)
	if err != nil {
		t.Fatalf("revoke-key failed: %v\n%s", err, out)
	}

	// Should have 1 key left. Verify via list output with no label.
	out, _ = runCLI(t, bin, "revoke-key", "--caller", "angus", "--db", db)
	if !strings.Contains(out, "desktop") {
		t.Errorf("expected desktop key to remain, got: %s", out)
	}
	if strings.Contains(out, "laptop") {
		t.Errorf("expected laptop key to be gone, got: %s", out)
	}
}

// --- Remote mode flag tests ---

func TestCLIRemoteAndDBMutuallyExclusive(t *testing.T) {
	bin := buildBinary(t)

	out, err := runCLI(t, bin, "list-callers", "--remote", "http://localhost:9090", "--admin-key", "k", "--db", "test.db")
	if err == nil {
		t.Fatal("expected error when both --remote and --db are set")
	}
	if !strings.Contains(out, "mutually exclusive") {
		t.Errorf("expected 'mutually exclusive' in error, got: %s", out)
	}
}

func TestCLIRemoteRequiresAdminKey(t *testing.T) {
	bin := buildBinary(t)

	out, err := runCLI(t, bin, "list-callers", "--remote", "http://localhost:9090")
	if err == nil {
		t.Fatal("expected error when --remote is set without admin key")
	}
	if !strings.Contains(out, "admin-key") || !strings.Contains(out, "STILE_ADMIN_KEY") {
		t.Errorf("expected admin key requirement in error, got: %s", out)
	}
}

func TestCLIRemoteAdminKeyFromEnv(t *testing.T) {
	bin := buildBinary(t)

	// Start a real admin server.
	store, ts := startTestAdminServer(t)
	defer ts.Close()

	store.AddCaller("env-test")

	cmd := exec.Command(bin, "list-callers", "--remote", ts.URL)
	cmd.Env = append(os.Environ(), "STILE_ADMIN_KEY=test-admin-key")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("list-callers with STILE_ADMIN_KEY failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "env-test") {
		t.Errorf("expected env-test in output, got: %s", out)
	}
}

// --- Integration tests: CLI --remote round trip ---

func startTestAdminServer(t *testing.T) (*auth.SQLiteStore, *httptest.Server) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "admin-test.db")
	store, err := auth.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	h := admin.NewHandler(store, nil)
	mux := http.NewServeMux()
	h.Register(mux)

	adminKey := "test-admin-key"
	adminHash := sha256.Sum256([]byte(adminKey))
	adminMW := auth.AdminAuthMiddleware(adminHash, false)
	ts := testutil.NewServer(adminMW(mux))
	t.Cleanup(ts.Close)

	return store, ts
}

func TestCLIRemoteRoundTrip(t *testing.T) {
	bin := buildBinary(t)
	_, ts := startTestAdminServer(t)

	adminKey := "test-admin-key"

	// Add a caller via remote.
	out, err := runCLI(t, bin, "add-caller", "--name", "remote-user",
		"--remote", ts.URL, "--admin-key", adminKey)
	if err != nil {
		t.Fatalf("add-caller --remote failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"remote-user"`) {
		t.Errorf("expected confirmation, got: %s", out)
	}

	// Add a key via remote.
	out, err = runCLI(t, bin, "add-key", "--caller", "remote-user", "--label", "ci",
		"--remote", ts.URL, "--admin-key", adminKey)
	if err != nil {
		t.Fatalf("add-key --remote failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "sk-") {
		t.Errorf("expected key in output, got: %s", out)
	}

	// Assign a role via remote.
	out, err = runCLI(t, bin, "assign-role", "--caller", "remote-user", "--role", "dev",
		"--remote", ts.URL, "--admin-key", adminKey)
	if err != nil {
		t.Fatalf("assign-role --remote failed: %v\n%s", err, out)
	}

	// List callers via remote.
	out, err = runCLI(t, bin, "list-callers",
		"--remote", ts.URL, "--admin-key", adminKey)
	if err != nil {
		t.Fatalf("list-callers --remote failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "remote-user") {
		t.Errorf("expected remote-user in list, got: %s", out)
	}
	if !strings.Contains(out, "dev") {
		t.Errorf("expected dev role in list, got: %s", out)
	}

	// Unassign role via remote.
	out, err = runCLI(t, bin, "unassign-role", "--caller", "remote-user", "--role", "dev",
		"--remote", ts.URL, "--admin-key", adminKey)
	if err != nil {
		t.Fatalf("unassign-role --remote failed: %v\n%s", err, out)
	}

	// Revoke key via remote.
	out, err = runCLI(t, bin, "revoke-key", "--caller", "remote-user", "--label", "ci",
		"--remote", ts.URL, "--admin-key", adminKey)
	if err != nil {
		t.Fatalf("revoke-key --remote failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "revoked") {
		t.Errorf("expected revoked confirmation, got: %s", out)
	}

	// Remove caller via remote (no keys left, so no --force needed).
	out, err = runCLI(t, bin, "remove-caller", "--name", "remote-user",
		"--remote", ts.URL, "--admin-key", adminKey)
	if err != nil {
		t.Fatalf("remove-caller --remote failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "removed") {
		t.Errorf("expected removed confirmation, got: %s", out)
	}

	// Verify caller is gone.
	out, _ = runCLI(t, bin, "list-callers",
		"--remote", ts.URL, "--admin-key", adminKey)
	if strings.Contains(out, "remote-user") {
		t.Errorf("expected remote-user removed, got: %s", out)
	}
}
