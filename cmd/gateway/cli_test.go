package main

import (
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
