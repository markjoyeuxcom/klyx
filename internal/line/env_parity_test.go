package line

import (
	"os"
	"regexp"
	"strconv"
	"testing"
)

// The frontend ranks environments itself for station order (The Line slice 4
// §2.2: the order must hold before the first LineDTO and for clusters with no
// Observer). Its ENV_RANK must match this table exactly.
func TestEnvRankMatchesTheFrontend(t *testing.T) {
	src, err := os.ReadFile("../../cmd/klyx/frontend/src/nav/stationOrder.ts")
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
