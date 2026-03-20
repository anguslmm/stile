// seed-caller.go — creates a test caller and prints an API key.
//
// Usage: go run scripts/seed-caller.go [db-path]
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

	// Create caller with access to echo only.
	_ = store.AddCaller("alice", []string{"echo"})
	// Create caller with access to everything.
	_ = store.AddCaller("bob", []string{"*"})

	aliceKey := "sk-" + generateKey()
	aliceHash := sha256.Sum256([]byte(aliceKey))
	if err := store.AddKey("alice", aliceHash, "dev", "alice-dev"); err != nil {
		log.Fatal(err)
	}

	bobKey := "sk-" + generateKey()
	bobHash := sha256.Sum256([]byte(bobKey))
	if err := store.AddKey("bob", bobHash, "dev", "bob-dev"); err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Database: %s\n\n", dbPath)
	fmt.Printf("alice (allowed: echo only):\n  %s\n\n", aliceKey)
	fmt.Printf("bob   (allowed: *):\n  %s\n\n", bobKey)
	fmt.Println("Use these as: Authorization: Bearer <key>")
}

func generateKey() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
