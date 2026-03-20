// seed-caller.go — creates test callers and prints API keys.
//
// Usage: go run cmd/seed/main.go [db-path]
// Default db-path: /tmp/stile-test.db
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"

	"github.com/anguslmm/stile/internal/auth"
)

func main() {
	dbPath := "/tmp/stile-test.db"
	if len(os.Args) > 1 {
		dbPath = os.Args[1]
	}

	store, err := auth.NewSQLiteStore(dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()

	// Create callers.
	_ = store.AddCaller("alice")
	_ = store.AddCaller("bob")
	_ = store.AddCaller("charlie")

	// Assign roles to callers (roles are on callers, not keys).
	_ = store.AssignRole("alice", "http-only")
	_ = store.AssignRole("alice", "stdio-only")
	_ = store.AssignRole("bob", "full-access")
	_ = store.AssignRole("charlie", "http-only")

	// One key per caller is sufficient.
	aliceKey := "sk-" + generateKey()
	aliceHash := sha256.Sum256([]byte(aliceKey))
	if err := store.AddKey("alice", aliceHash, "alice-key"); err != nil {
		log.Fatal(err)
	}

	bobKey := "sk-" + generateKey()
	bobHash := sha256.Sum256([]byte(bobKey))
	if err := store.AddKey("bob", bobHash, "bob-key"); err != nil {
		log.Fatal(err)
	}

	charlieKey := "sk-" + generateKey()
	charlieHash := sha256.Sum256([]byte(charlieKey))
	if err := store.AddKey("charlie", charlieHash, "charlie-key"); err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Database: %s\n\n", dbPath)
	fmt.Printf("alice (roles: http-only, stdio-only):\n")
	fmt.Printf("  %s\n\n", aliceKey)
	fmt.Printf("bob (roles: full-access):\n")
	fmt.Printf("  %s\n\n", bobKey)
	fmt.Printf("charlie (roles: http-only):\n")
	fmt.Printf("  %s\n\n", charlieKey)
	fmt.Println("Use these as: Authorization: Bearer <key>")
}

func generateKey() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
