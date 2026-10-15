package fleet

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/fleetindex"
)

// The Line slice 9a against the kind lab, read-only, through the
// production connection:
//
//	KLYX_LAB_CONTEXT=kind-klyx go test ./internal/fleet -run TestLabIndex -v -count=1
//
// Every default kind reaches a settled state; the core kinds hold entries;
// nothing is denied or unsupported for the lab's admin; Secrets are read as
// metadata (a content answer would read unsupported).
func TestLabIndex(t *testing.T) {
	ctxName := os.Getenv("KLYX_LAB_CONTEXT")
	if ctxName == "" {
		t.Skip("set KLYX_LAB_CONTEXT=kind-klyx")
	}
	if !strings.HasPrefix(ctxName, "kind-") {
		t.Fatalf("refusing %q: the lab runs only against kind-* contexts", ctxName)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn, err := DefaultConnFactory(clock.Real{})(config.ClusterConfig{Name: ctxName, Context: ctxName})
	if err != nil {
		t.Fatal(err)
	}
	c := conn.(*ClusterConn)
	c.SetHealthSink(&labSink{})
	ix := fleetindex.New(fleetindex.Settings{Gaps: IndexGaps()})
	defer ix.Stop()
	ix.SetClusters([]string{ctxName})
	c.SetIndex(ix)
	start := time.Now()
	c.Start(ctx)
	h := c.Health()
	defer func() { cancel(); <-h.Done() }()
	settled := func(st string) bool { return st != fleetindex.StateIndexing && st != "" }
	var cov fleetindex.ClusterCoverage
	waitLab(t, 120*time.Second, "every kind settled", func() bool {
		cov = ix.Coverage()[0]
		if !cov.Indexed {
			return false
		}
		for _, k := range cov.Kinds {
			if !settled(k.State) {
				return false
			}
		}
		return true
	})
	t.Logf("settled in %v; connection %q, incarnation %d", time.Since(start).Round(time.Millisecond), cov.Connection, cov.Incarnation)
	entries := map[string]int{}
	for _, k := range cov.Kinds {
		t.Logf("%-50s %-12s %6d %s", k.Key, k.State, k.Entries, k.Err)
		entries[k.Key] = k.Entries
		switch k.State {
		case fleetindex.StateDenied, fleetindex.StateUnsupported, fleetindex.StateOversize, fleetindex.StateWatchDenied, fleetindex.StateCapped:
			t.Errorf("%s: %s (%s)", k.Key, k.State, k.Err)
		}
	}
	for _, k := range []string{"/pods", "/namespaces", "/configmaps", "/secrets", "apps/deployments", "apps/replicasets", "/services"} {
		if entries[k] == 0 {
			t.Errorf("%s: no entries", k)
		}
	}
	e, b, fe, fb := ix.Used(ctxName)
	t.Logf("budget: cluster %d entries %d KiB; fleet %d entries %d KiB", e, b>>10, fe, fb>>10)
}
