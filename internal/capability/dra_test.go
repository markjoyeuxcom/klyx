package capability

import (
	"context"
	"errors"
	"testing"
)

// Coverage S11b §5: the DRA lens appears where resource.k8s.io/v1 is served
// and some DRA object exists; a failed list is never read as absence.
func TestDetectDRA(t *testing.T) {
	served := map[string]bool{"resource.k8s.io/v1": true}
	one, none := []byte(`{"items":[{}]}`), []byte(`{"items":[]}`)
	cases := []struct {
		name    string
		served  map[string]bool
		lists   map[string][]byte // nil: the list fails
		present bool
		tier    Tier
		reason  string
		listed  int
	}{
		{"not served: nothing listed", map[string]bool{}, nil, false, Absent, "", 0},
		{"served, no object anywhere", served, map[string][]byte{"resourceslices": none, "resourceclaims": none, "deviceclasses": none}, false, Absent, "", 3},
		{"a claim exists: present, the classes never listed", served, map[string][]byte{"resourceslices": none, "resourceclaims": one}, true, Absent, "", 2},
		{"a slice exists although a later list would fail", served, map[string][]byte{"resourceslices": one}, true, Absent, "", 1},
		{"one list fails, the others empty: present, unverified", served, map[string][]byte{"resourceclaims": none, "deviceclasses": none}, true, Degraded, "could not list resourceslices", 3},
		{"every list fails", served, map[string][]byte{}, true, Degraded, "could not list resourceslices, resourceclaims, deviceclasses", 3},
		{"an unreadable answer is a failure", served, map[string][]byte{"resourceslices": []byte("<html>"), "resourceclaims": none, "deviceclasses": none}, true, Degraded, "could not list resourceslices", 3},
	}
	for _, c := range cases {
		listed := 0
		d := &Detector{rawList: func(_ context.Context, path string, params map[string]string) ([]byte, error) {
			listed++
			if params["limit"] != "1" {
				t.Errorf("%s: %s listed without a limit", c.name, path)
			}
			for r, b := range c.lists {
				if path == "/apis/resource.k8s.io/v1/"+r && b != nil {
					return b, nil
				}
			}
			return nil, errors.New("forbidden")
		}}
		got := d.detectDRA(context.Background(), c.served)
		if got.Present != c.present || got.Tier != c.tier || got.Reason != c.reason || listed != c.listed {
			t.Errorf("%s: %+v after %d lists", c.name, got, listed)
		}
	}
}
