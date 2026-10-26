package helmcli

import (
	"context"
	"errors"
	"testing"

	"github.com/moomora/klyx/internal/readfence"
)

// Settings slice F §2: a write's ticket is checked at the helm child's
// actual start; a refused start runs nothing.
func TestRunChecksTheFenceAtTheChildsStart(t *testing.T) {
	ctx := readfence.With(context.Background(), func() error { return errors.New("aks-we-prd: its policy changed") })
	if _, err := (ExecRunner{}).Run(ctx, "version"); !readfence.Failed(err) {
		t.Fatalf("a fenced start ran: %v", err)
	}
}
