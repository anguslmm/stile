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

	// Create callers (named identities — roles are on keys, not callers).
	_ = store.AddCaller("alice")
	_ = store.AddCaller("bob")
	_ = store.AddCaller("charlie")

	// Alice gets TWO role keys: http-only + stdio-only → union sees all tools.
	aliceHTTPKey := "sk-" + generateKey()
	aliceHTTPHash := sha256.Sum256([]byte(aliceHTTPKey))
	if err := store.AddKey("alice", aliceHTTPHash, "http-only", "alice-http"); err != nil {
		log.Fatal(err)
	}

	aliceStdioKey := "sk-" + generateKey()
	aliceStdioHash := sha256.Sum256([]byte(aliceStdioKey))
	if err := store.AddKey("alice", aliceStdioHash, "stdio-only", "alice-stdio"); err != nil {
		log.Fatal(err)
	}

	// Bob gets a "full-access" role key.
	bobKey := "sk-" + generateKey()
	bobHash := sha256.Sum256([]byte(bobKey))
	if err := store.AddKey("bob", bobHash, "full-access", "bob-full"); err != nil {
		log.Fatal(err)
	}

	// Charlie gets only an "http-only" key → can only see echo + add.
	charlieKey := "sk-" + generateKey()
	charlieHash := sha256.Sum256([]byte(charlieKey))
	if err := store.AddKey("charlie", charlieHash, "http-only", "charlie-http"); err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Database: %s\n\n", dbPath)
	fmt.Printf("alice (roles: http-only + stdio-only):\n")
	fmt.Printf("  http-only key:  %s\n", aliceHTTPKey)
	fmt.Printf("  stdio-only key: %s\n\n", aliceStdioKey)
	fmt.Printf("bob (role: full-access):\n")
	fmt.Printf("  %s\n\n", bobKey)
	fmt.Printf("charlie (role: http-only):\n")
	fmt.Printf("  %s\n\n", charlieKey)
	fmt.Println("Use these as: Authorization: Bearer <key>")
}

func generateKey() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
