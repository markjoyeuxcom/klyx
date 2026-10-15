package line

import (
	"os"
	"regexp"
	"strconv"
	"testing"
)

// The frontend's FleetSpine still ranks environments itself until slice 4
// reads ranks from LineDTO: its ENV_RANK must match this table exactly.
func TestEnvRankMatchesTheFrontend(t *testing.T) {
	src, err := os.ReadFile("../../cmd/klyx/frontend/src/chrome/FleetSpine.tsx")
	if err != nil {
		t.Fatal(err)
	}
	rows := regexp.MustCompile(`\[/(\^\([^/]+\)\$)/, (\d)\]`).FindAllStringSubmatch(string(src), -1)
	if len(rows) != len(envRanks) {
		t.Fatalf("ENV_RANK has %d rows, Go has %d", len(rows), len(envRanks))
	}
	for i, r := range rows {
		rank, _ := strconv.Atoi(r[2])
		if r[1] != envRanks[i].re.String() || rank != envRanks[i].rank {
			t.Errorf("row %d: frontend %s=%d, Go %s=%d", i, r[1], rank, envRanks[i].re, envRanks[i].rank)
		}
	}
}
