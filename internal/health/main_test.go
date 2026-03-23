package health

import (
	"fmt"
	"os"
	"testing"

	"github.com/alicebob/miniredis/v2"
)

// sharedMR is a single miniredis instance shared across all Redis health store
// tests. Tests call sharedMR.FlushAll() before each use to ensure isolation.
var sharedMR *miniredis.Miniredis

func TestMain(m *testing.M) {
	var err error
	sharedMR, err = miniredis.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "start shared miniredis: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	sharedMR.Close()
	os.Exit(code)
}
