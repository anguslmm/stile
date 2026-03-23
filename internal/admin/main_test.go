package admin

import (
	"os"
	"testing"

	"github.com/anguslmm/stile/internal/testutil"
)

func TestMain(m *testing.M) {
	testutil.PatchDefaultTransport()
	os.Exit(m.Run())
}
