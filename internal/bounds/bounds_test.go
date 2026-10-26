package bounds

import "testing"

func TestListIsUniqueAndPositive(t *testing.T) {
	seen := map[string]bool{}
	for _, b := range List() {
		if b.ID == "" || b.Value <= 0 || b.Unit == "" || seen[b.ID] {
			t.Fatalf("%+v", b)
		}
		seen[b.ID] = true
	}
}
