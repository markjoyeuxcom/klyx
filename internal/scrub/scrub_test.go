package scrub

import (
	"strings"
	"testing"
)

func TestCredentialsReplacesShapesWhole(t *testing.T) {
	in := "fetch https://alice:review-password@repo.invalid/a failed; token=review-credential; Authorization: Bearer abcdefghijklmnop"
	out, n := Credentials(in)
	for _, leak := range []string{"alice", "review-password", "review-credential", "abcdefghijklmnop"} {
		if strings.Contains(out, leak) {
			t.Fatalf("%q survived: %s", leak, out)
		}
	}
	if n < 3 || !strings.Contains(out, "https://[redacted]@repo.invalid/a") {
		t.Fatalf("replacements %d: %s", n, out)
	}
}
