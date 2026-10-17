package urlscrub

import (
	"strings"
	"testing"
)

// Coverage S1c §7 (revision 3): each needle is absent, and the kept cases
// keep their text before the URL and a well-formed origin.
func TestOrigins(t *testing.T) {
	const n = "SIGNEDneedle"
	withheld := []struct{ name, in string }{
		{"a signed query", "failed to download archive from https://store.example/a.tgz?sig=" + n},
		{"a signed path", "fetch https://store.example/t/" + n + "/a.tgz: 403"},
		{"an unindented continuation", "failed to fetch https://store.example/archive?sig=first\n" + n},
		{"an indented continuation", "failed to fetch https://store.example/archive?sig=first\n    " + n},
		{"several continuation lines", "failed https://store.example/a?x=1\nmore\n" + n + "\nend"},
		{"an escaped line break in a quoted URL", `Get "https://store.example/a?sig=x\n` + n + `": dial tcp`},
		{"a literal space, as a non-200 fetch error prints it", "failed to download archive from https://store.example/a?sig=ab " + n + " (status 404)"},
		{"user info with a space in its password", "https://user:12 " + n + "@store.example/a"},
		{"user info with a slash in its password", "https://user:p/" + n + "@store.example/a"},
		{"an invalid port", "https://store.example:" + n + "/a"},
		{"a second URL", "from https://store.example then https://other.example/" + n},
		{"a URL with no path but trailing text", "failed https://store.example " + n},
	}
	for _, c := range withheld {
		out, changed := Origins(c.in)
		if strings.Contains(out, n) || !changed {
			t.Errorf("%s: %q", c.name, out)
		}
	}
	kept := []struct{ in, want string }{
		{"failed to download archive from https://store.example/a.tgz?sig=x", "failed to download archive from https://store.example " + OriginsRest},
		{"source is https://store.example", "source is https://store.example"},
		{"HTTPS://Store.Example:8443/a", "HTTPS://Store.Example:8443 " + OriginsRest},
		{"at http://source-controller.flux-system.svc.cluster.local./gitrepository/x.tar.gz", "at http://source-controller.flux-system.svc.cluster.local. " + OriginsRest},
		{"https://user:pw@store.example/a", "https://store.example " + OriginsRest},
		{"https://user%40corp:pw%40x@store.example/a", "https://store.example " + OriginsRest},
		{"http://10.0.0.1:9090/x", "http://10.0.0.1:9090 " + OriginsRest},
		{"http://[fe80::1]:80/x", "http://[fe80::1]:80 " + OriginsRest},
		{"https:///path", "https://" + OriginWithheld + " " + OriginsRest},
		{"no url here", "no url here"},
	}
	for _, c := range kept {
		if out, _ := Origins(c.in); out != c.want {
			t.Errorf("%q: got %q, want %q", c.in, out, c.want)
		}
	}
}

// The rule is idempotent: its own output passes unchanged.
func TestOriginsIsIdempotent(t *testing.T) {
	for _, in := range []string{
		"failed to download archive from https://store.example/a.tgz?sig=x",
		"https://user:12 34@store.example/a", "https:///path", "x https://store.example:bad/a", "source is https://store.example",
	} {
		once, _ := Origins(in)
		if twice, changed := Origins(once); changed || twice != once {
			t.Errorf("%q: once %q, twice %q", in, once, twice)
		}
	}
}

// Codex's Stage 0 review, finding 2: a URL that is not "<scheme>://" is
// still withheld, from the request echo or bare "http(s):" that prints it;
// an echo whose target is a well-formed URL keeps its origin.
func TestOriginsMalformedURLs(t *testing.T) {
	const n = "SIGNEDneedle2M"
	for _, in := range []string{
		`failed to download archive: Get "https:/store.example/signed/` + n + `": http: no Host in request URL`,
		`failed to download archive: Get "/signed/` + n + `": unsupported protocol scheme ""`,
		`failed to download archive: GET /signed/` + n + ` giving up after 1 attempt(s): Get "/signed/` + n + `": unsupported protocol scheme ""`,
		`failed to download archive: GET ` + n + ` giving up after 1 attempt(s)`,
		`failed to create a new request: parse "https:/x` + n + "\x7f" + `": net/url: invalid control character in URL`,
		`failed to download archive from https:/store.example/` + n + ` (status: 403)`,
		`fetching HTTPS:store.example/` + n,
		`Could not load chart: Head "oci:/` + n + `": unsupported protocol scheme "oci"`,
	} {
		out, changed := Origins(in)
		if strings.Contains(out, n) || !changed || !strings.HasSuffix(out, OriginsRest) {
			t.Errorf("%q: %q", in, out)
		}
		if twice, c := Origins(out); c || twice != out {
			t.Errorf("not idempotent: %q then %q", out, twice)
		}
	}
	for in, want := range map[string]string{
		`failed to download archive: GET https://store.example/a?sig=` + n + ` giving up after 3 attempt(s)`: "failed to download archive: GET https://store.example " + OriginsRest,
		`failed to download archive: Get "http://source-controller.flux-system.svc/x": dial tcp`:             `failed to download archive: Get "http://source-controller.flux-system.svc ` + OriginsRest,
		`failed to download archive: ` + n: `failed to download archive: ` + n,
		"TARGET reached; the parsed value": "TARGET reached; the parsed value",
	} {
		if out, _ := Origins(in); out != want {
			t.Errorf("%q: got %q, want %q", in, out, want)
		}
	}
	if p, ok := Prefix(`failed to download archive: GET https://h/x giving up`); !ok || p != "failed to download archive: " {
		t.Errorf("an echo's method is not part of its prefix: %q %v", p, ok)
	}
	if _, ok := Prefix("no url here"); ok {
		t.Error("no candidate, no prefix")
	}
}
