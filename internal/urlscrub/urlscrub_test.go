package urlscrub

import (
	"os"
	"strings"
	"testing"
)

const needle = "NEEDLEcred7Q"

// kubectlShorten is kubectl's describe.shorten at maxAnnotationLen-2 (138):
// the first 138 bytes, then "...".
func kubectlShorten(s string) string {
	if len(s) > 138 {
		return s[:138] + "..."
	}
	return s
}

func TestText(t *testing.T) {
	cases := []struct {
		name, in, want string
		removed        int
		withheld       int
	}{
		{"no credentials", "clone https://github.com/org/repo.git done", "clone https://github.com/org/repo.git done", 0, 0},
		{"user and password", "https://bob:" + needle + "@github.com/org/repo", "https://github.com/org/repo", 1, 0},
		{"token as user", "https://" + needle + "@github.com/org/repo", "https://github.com/org/repo", 1, 0},
		{"git over ssh kept", "ssh://git@github.com:22/org/repo", "ssh://git@github.com:22/org/repo", 0, 0},
		{"git with a password", "https://git:" + needle + "@host/r", "https://host/r", 1, 0},
		{"percent-encoded user info", "https://us%65r:" + needle + "%40x@host/r", "https://host/r", 1, 0},
		{"multiple @", "https://a@b:" + needle + "@host/r", "https://host/r", 1, 0},
		{"quoted in a condition", "unable to clone 'https://x-access-token:" + needle + "@github.com/org/repo': auth", "unable to clone 'https://github.com/org/repo': auth", 1, 0},
		{"port kept", "https://example.com:8443/x", "https://example.com:8443/x", 0, 0},
		{"port-less colon redacted", "see https://user:" + needle + " now", "see https://" + Redacted + " now", 1, 0},
		{"IPv6 with port kept", "https://[::1]:8080/x", "https://[::1]:8080/x", 0, 0},
		{"IPv6 unbalanced withheld", "https://[::1:" + needle + "/x", WithheldLine, 0, 1},
		{"control character withheld", "https://bo\x01b:" + needle + "@h/x", WithheldLine, 0, 1},
		{"scp user removed", "repo bob@github.com:org/repo.git", "repo github.com:org/repo.git", 1, 0},
		{"scp git kept", "repo git@github.com:org/repo.git", "repo git@github.com:org/repo.git", 0, 0},
		{"two URLs", "a https://u:" + needle + "@h1/x b https://h2/y", "a https://h1/x b https://h2/y", 1, 0},
	}
	for _, c := range cases {
		r := Text(c.in)
		if r.Text != c.want || r.Removed != c.removed || r.Withheld != c.withheld {
			t.Errorf("%s:\n got %q (removed %d, withheld %d)\nwant %q (removed %d, withheld %d)", c.name, r.Text, r.Removed, r.Withheld, c.want, c.removed, c.withheld)
		}
		if strings.Contains(r.Text, needle) {
			t.Errorf("%s: the needle survived: %q", c.name, r.Text)
		}
	}
}

// kubectl cuts a long annotation line at 138 bytes plus "...": a URL whose
// credential runs past the cut loses its "@", and must still be redacted
// (Codex's S0b review of 9c3a1d9).
func TestText_KubectlAnnotationTruncation(t *testing.T) {
	for _, at := range []int{20, 60, 120, 135} {
		cred := strings.Repeat("Q", at) + needle + strings.Repeat("Z", 200)
		line := "  example.com/source: https://bob:" + cred + "@git.example.com/org/repo.git"
		cut := kubectlShorten(line)
		if strings.Contains(cut, "@") {
			t.Fatalf("fixture: the cut at %d kept the @", at)
		}
		r := Text(cut)
		if strings.Contains(r.Text, "QQQ") || strings.Contains(r.Text, needle) || !r.Changed() {
			t.Errorf("cut at %d: credential bytes survived: %q", at, r.Text)
		}
	}
	// cut inside the host after the @: still redacted, never half-kept
	line := "  example.com/source: https://bob:" + needle + "@" + strings.Repeat("h", 200) + ".example.com/r"
	if r := Text(kubectlShorten(line)); strings.Contains(r.Text, needle) || strings.Contains(r.Text, "bob") {
		t.Errorf("cut in the host: %q", r.Text)
	}
}

func TestText_Multiline(t *testing.T) {
	in := "Url: https://bob:" + needle + "@h/x\nReady: True\nBad: https://[::1:" + needle
	r := Text(in)
	lines := strings.Split(r.Text, "\n")
	if lines[0] != "Url: https://h/x" || lines[1] != "Ready: True" || lines[2] != WithheldLine {
		t.Fatalf("lines: %q", lines)
	}
	if r.Removed != 1 || r.Withheld != 1 {
		t.Fatalf("counts: %+v", r)
	}
}

func TestField(t *testing.T) {
	if r := Field("https://[::1:" + needle); r.Text != WithheldURL || !r.Changed() {
		t.Fatalf("an undelimitable URL field is withheld whole: %+v", r)
	}
	if r := Field("https://github.com/org/repo"); r.Text != "https://github.com/org/repo" || r.Changed() {
		t.Fatalf("a clean field is unchanged: %+v", r)
	}
}

func TestText_Idempotent(t *testing.T) {
	for _, in := range []string{"https://bob:" + needle + "@h/x", "https://user:" + needle, "https://[::1:x", "bob@github.com:o/r"} {
		once := Text(in).Text
		if twice := Text(once); twice.Text != once || twice.Changed() {
			t.Errorf("not idempotent for %q: %q then %q", in, once, twice.Text)
		}
	}
}

// Real kubectl output (captured from `kubectl describe gitrepository` on the
// kind lab, kubectl v1.37): an annotation holding a credential URL longer
// than 138 bytes is cut with "..." and loses its "@", and spec.url carries
// a token. No credential byte may survive (coverage S0b §4).
func TestText_RealKubectlDescribe(t *testing.T) {
	raw, err := os.ReadFile("testdata/describe-gitrepository.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "lab-only-annot-token") || !strings.Contains(string(raw), "...") {
		t.Fatal("fixture: the captured output must hold the truncated annotation")
	}
	r := Text(string(raw))
	for _, m := range []string{"lab-only-annot-token", "lab-only-url-token", "x-access-token", "bob:"} {
		if strings.Contains(r.Text, m) {
			t.Errorf("%q survived:\n%s", m, r.Text)
		}
	}
	if !r.Changed() || !strings.Contains(r.Text, "https://github.com/org/fleet.git") {
		t.Fatalf("scrubbed output: %s", r.Text)
	}
}
