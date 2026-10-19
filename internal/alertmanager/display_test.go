package alertmanager

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/moomora/klyx/internal/safehttp"
)

// every display string of an alert, flattened for a leak check
func displayed(d DisplayAlert) string {
	b, _ := json.Marshal(d)
	var s strings.Builder
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			s.WriteString(x)
			s.WriteString("\n")
		case []any:
			for _, e := range x {
				walk(e)
			}
		case map[string]any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	var v any
	_ = json.Unmarshal(b, &v)
	walk(v)
	return s.String()
}

// S8a §3, §10: on every surface, the credential literal, JSON-escaped in the
// response body, and in its quoted spelling never reaches a display value;
// the raw values still match, deduplicate and correlate.
func TestDisplayRedactsEverySurface(t *testing.T) {
	const secret = `S8/NEEDLE"q\z` // a slash for JSON's /, a quote and a backslash for the quoted spelling
	red := safehttp.NewRedactor(safehttp.Bearer(secret))
	q := strconv.Quote(secret)
	quoted := q[1 : len(q)-1] // the quoted spelling, as an error or a template writes it
	jsonEsc := strings.ReplaceAll(strings.Trim(string(must(json.Marshal(secret))), `"`), "/", `/`)
	// the body carries the JSON-escaped form; decoding makes it literal
	body := fmt.Sprintf(`[{"fingerprint":"fp-%[1]s","labels":{"alertname":"A-%[1]s","severity":"critical","namespace":"team","pod":"p-%[1]s","note-%[1]s":"v-%[1]s"},
	  "annotations":{"summary":"sum %[1]s","description":"desc %[1]s, quoted %[2]s","runbook_url":"https://u:%[1]s@runbooks.example/r?token=%[1]s"},
	  "status":{"state":"active","silencedBy":["sil-%[1]s"],"inhibitedBy":[],"mutedBy":[]},
	  "receivers":[{"name":"rcv-%[1]s"}],"generatorURL":"https://user:pw@prom.example/graph?x=%[1]s"},
	 {"fingerprint":"g2","labels":{"alertname":"B"},"status":{"state":"state-%[1]s"},"receivers":[]}]`, jsonEsc, strings.ReplaceAll(strings.ReplaceAll(quoted, `\`, `\\`), `"`, `\"`))
	alerts, err := ParseAlerts([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(alerts[0].Labels["pod"], secret) {
		t.Fatalf("the JSON-escaped needle did not decode: %q", alerts[0].Labels["pod"])
	}
	groups := []Group{{Receiver: "rcv-" + secret, Alerts: []GroupAlert{{Fingerprint: "fp-" + secret, State: StateActive}}}}
	reduced := Reduce(alerts, groups)
	for _, r := range reduced {
		d := Display(r, red)
		out := displayed(d)
		for _, needle := range []string{secret, quoted, "S8/NEEDLE", `S8/NEEDLE`} {
			if strings.Contains(out, needle) {
				t.Fatalf("%q reached a display value:\n%s", needle, out)
			}
		}
	}
	d := Display(reduced[0], red)
	if strings.Contains(d.GeneratorURL, "user") || strings.Contains(d.GeneratorURL, "pw") || !strings.HasPrefix(d.GeneratorURL, "https://prom.example/graph") {
		t.Fatalf("generatorURL: %q", d.GeneratorURL)
	}
	// this needle's slash breaks the URL's parse, so the userinfo is the
	// general shape's to replace
	if strings.Contains(d.RunbookURL, "u:") || !strings.Contains(d.RunbookURL, "runbooks.example/r?token=[redacted]") || strings.Contains(d.RunbookURL, "]]") {
		t.Fatalf("runbook_url: %q", d.RunbookURL)
	}
	if got := CleanURL("https://user:pw@prom.example/g?x=1", red); got != "https://prom.example/g?x=1" {
		t.Fatalf("a parsing URL loses its userinfo whole: %q", got)
	}
	// raw values are unchanged, and still correlate and match
	r := reduced[0]
	if s := SubjectOf(r.Labels); s.Kind != "Pod" || s.Name != "p-"+secret {
		t.Fatalf("raw correlation: %+v", s)
	}
	if !Matches(r.Labels, map[string]string{"pod": "p-" + secret}) || r.Mute.Kind != MuteNone {
		t.Fatalf("raw match or reduce: %+v", r.Mute)
	}
}

// A credential that equals a namespace, a pod name or a fingerprint is
// redacted from the display values only; the raw keys still serve.
func TestDisplayCredentialEqualToAKey(t *testing.T) {
	for _, secret := range []string{"team", "api-1", "f00dfeed"} {
		red := safehttp.NewRedactor(safehttp.Bearer(secret))
		a := Alert{Fingerprint: "f00dfeed", Labels: map[string]string{"alertname": "A", "namespace": "team", "pod": "api-1"}, State: StateActive, Receivers: []string{"r"}}
		r := Reduce([]Alert{a}, []Group{group("r", live("f00dfeed"))})[0]
		d := Display(r, red)
		if strings.Contains(displayed(d), secret) {
			t.Fatalf("%q displayed", secret)
		}
		if s := SubjectOf(r.Labels); s.Namespace != "team" || s.Name != "api-1" || r.Fingerprint != "f00dfeed" || r.Mute.Kind != MuteNone {
			t.Fatalf("%q: raw keys changed: %+v", secret, s)
		}
	}
}

// Clipping comes after redaction: a credential across the cap is replaced
// whole, never cut into a readable prefix.
func TestDisplayClipsAfterRedaction(t *testing.T) {
	const secret = "CLIPNEEDLE-0123456789"
	red := safehttp.NewRedactor(safehttp.Bearer(secret))
	summary := strings.Repeat("a", MaxSummary-10) + secret
	r := Reduced{Alert: Alert{Fingerprint: "f", Labels: map[string]string{}, Annotations: map[string]string{"summary": summary}}, State: StateActive}
	d := Display(r, red)
	if len(d.Summary) > MaxSummary || strings.Contains(d.Summary, "CLIPN") {
		t.Fatalf("summary: %d bytes, %q", len(d.Summary), d.Summary[len(d.Summary)-30:])
	}
}

// The caps: 64 labels and annotations shown with the rest counted, values
// within their byte caps.
func TestDisplayCaps(t *testing.T) {
	labels := map[string]string{}
	for i := 0; i < 70; i++ {
		labels[fmt.Sprintf("l%02d", i)] = strings.Repeat("v", 300)
	}
	ann := map[string]string{"summary": strings.Repeat("s", 600), "description": strings.Repeat("d", 5000)}
	r := Reduced{Alert: Alert{Fingerprint: "f", Labels: labels, Annotations: ann, GeneratorURL: "https://p.example/" + strings.Repeat("x", 3000)}, State: StateActive}
	d := Display(r, safehttp.NewRedactor())
	if len(d.Labels) != MaxPairs || d.LabelsMore != 6 || d.Labels[0].Name != "l00" {
		t.Fatalf("labels: %d more %d", len(d.Labels), d.LabelsMore)
	}
	for _, p := range d.Labels {
		if len(p.Value) > MaxLabelValue {
			t.Fatalf("label value %d bytes", len(p.Value))
		}
	}
	if len(d.Summary) > MaxSummary || len(d.Description) > MaxAnnotation || len(d.GeneratorURL) > MaxURL {
		t.Fatalf("caps: %d %d %d", len(d.Summary), len(d.Description), len(d.GeneratorURL))
	}
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}
