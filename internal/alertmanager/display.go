package alertmanager

import (
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/moomora/klyx/internal/safehttp"
)

// Display caps (S8a §8), applied after redaction, never before.
const (
	MaxPairs      = 64   // labels, and annotations, shown per alert
	MaxLabelValue = 256  // bytes per label name or value
	MaxSummary    = 512  // bytes of the summary annotation
	MaxAnnotation = 4096 // bytes of any other annotation
	MaxURL        = 2048 // bytes per URL
)

// Pair is a displayed label or annotation.
type Pair struct{ Name, Value string }

// DisplaySubject is a subject as shown.
type DisplaySubject struct {
	Kind      string
	Namespace string
	Name      string
	Container string
	Basis     []string
	AlsoNames []DisplaySubject
	// Exact: the namespace and name are shown as written - neither redacted
	// nor clipped - so a consumer may compare them with an object's name.
	// Never inferred from the text of a redaction marker, which a clip can cut.
	Exact bool
}

// DisplayMute is an active alert's mute evidence as shown.
type DisplayMute struct {
	Kind      MuteKind
	MutedFor  []string
	ActiveFor []string
	Expected  []string
	Shown     []string
}

// DisplayAlert is everything about an alert that may leave this package
// (S8a §3): every string made in C5's order - the reading transport's own
// redactor (exact forms and quoted spellings), then the general credential
// shapes, then URLs without userinfo, then the clip. Times are not text.
type DisplayAlert struct {
	Fingerprint string
	Name        string // the alertname label
	Severity    string // the severity label as written ("" for none)
	Counted     bool
	State       string // the state as written, a display value
	// StateClass and SeverityClass are Klyx's classes, read from the raw
	// alert: every count, order and role uses them, never the display text,
	// which a credential equal to "active" or "critical" would redact.
	StateClass      string
	SeverityClass   Severity
	Summary         string
	Description     string
	RunbookURL      string
	GeneratorURL    string
	Labels          []Pair
	LabelsMore      int // labels past MaxPairs
	Annotations     []Pair
	AnnotationsMore int
	Receivers       []string
	SilencedBy      []string
	InhibitedBy     []string
	MutedBy         []string
	Mute            DisplayMute
	Disagreement    string
	Subject         DisplaySubject
	StartsAt        time.Time
	UpdatedAt       time.Time
	EndsAt          time.Time
}

// Display makes an alert's display values with the redactor of the
// transport that read it: a response can only echo the credential it was
// sent.
func Display(r Reduced, red safehttp.Redactor) DisplayAlert {
	t := func(s string, max int) string { return Clip(red.Text(s), max) }
	ts := func(ss []string) []string {
		if len(ss) == 0 {
			return nil
		}
		out := make([]string, len(ss))
		for i, s := range ss {
			out[i] = t(s, MaxLabelValue)
		}
		return out
	}
	d := DisplayAlert{
		Fingerprint:   t(r.Fingerprint, MaxLabelValue),
		Name:          t(r.Labels["alertname"], MaxLabelValue),
		Severity:      t(r.Labels["severity"], MaxLabelValue),
		Counted:       r.Counted(),
		State:         t(r.State, MaxLabelValue),
		StateClass:    StateClassOf(r.State),
		SeverityClass: SeverityOf(r.Labels),
		Summary:       t(r.Annotations["summary"], MaxSummary),
		Description:   t(r.Annotations["description"], MaxAnnotation),
		RunbookURL:    CleanURL(r.Annotations["runbook_url"], red),
		GeneratorURL:  CleanURL(r.GeneratorURL, red),
		Receivers:     ts(r.Receivers),
		SilencedBy:    ts(r.SilencedBy),
		InhibitedBy:   ts(r.InhibitedBy),
		MutedBy:       ts(r.MutedBy),
		Disagreement:  t(r.Disagreement, MaxLabelValue),
		Mute: DisplayMute{Kind: r.Mute.Kind, MutedFor: ts(r.Mute.MutedFor), ActiveFor: ts(r.Mute.ActiveFor),
			Expected: ts(r.Mute.Expected), Shown: ts(r.Mute.Shown)},
		Subject:   displaySubject(SubjectOf(r.Labels), t),
		StartsAt:  r.StartsAt,
		UpdatedAt: r.UpdatedAt,
		EndsAt:    r.EndsAt,
	}
	d.Labels, d.LabelsMore = pairs(r.Labels, func(k, v string) Pair { return Pair{t(k, MaxLabelValue), t(v, MaxLabelValue)} })
	d.Annotations, d.AnnotationsMore = pairs(r.Annotations, func(k, v string) Pair {
		switch {
		case k == "summary":
			return Pair{t(k, MaxLabelValue), t(v, MaxSummary)}
		case strings.HasSuffix(k, "_url"):
			return Pair{t(k, MaxLabelValue), CleanURL(v, red)}
		}
		return Pair{t(k, MaxLabelValue), t(v, MaxAnnotation)}
	})
	return d
}

// DisplayMatch is the configured match as it may leave the reader: names and
// values redacted with the reading transport's redactor, then clipped. The
// raw map stays with the reader for the filter and the identity.
func DisplayMatch(m map[string]string, red safehttp.Redactor) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[Clip(red.Text(k), MaxLabelValue)] = Clip(red.Text(v), MaxLabelValue)
	}
	return out
}

func displaySubject(s Subject, t func(string, int) string) DisplaySubject {
	d := DisplaySubject{Kind: s.Kind, Namespace: t(s.Namespace, MaxLabelValue), Name: t(s.Name, MaxLabelValue),
		Container: t(s.Container, MaxLabelValue), Basis: s.Basis}
	d.Exact = d.Namespace == s.Namespace && d.Name == s.Name
	for _, a := range s.AlsoNames {
		d.AlsoNames = append(d.AlsoNames, displaySubject(a, t))
	}
	return d
}

// pairs shows a map sorted by its raw keys, the first MaxPairs of them, and
// how many more there are.
func pairs(m map[string]string, mk func(k, v string) Pair) ([]Pair, int) {
	if len(m) == 0 {
		return nil, 0
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	more := 0
	if len(keys) > MaxPairs {
		more = len(keys) - MaxPairs
		keys = keys[:MaxPairs]
	}
	out := make([]Pair, len(keys))
	for i, k := range keys {
		out[i] = mk(k, m[k])
	}
	return out, more
}

// CleanURL is a URL as shown: the credential redacted, userinfo removed when
// it parses as http(s), the general shapes scrubbed, then clipped. Klyx
// shows it as text and never opens it.
func CleanURL(raw string, red safehttp.Redactor) string {
	if raw == "" {
		return ""
	}
	s := raw
	// userinfo goes on the raw parse; a URL that does not parse keeps only
	// what the redactor and the general shapes (userinfo among them) leave
	if u, err := url.Parse(raw); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
		u.User = nil
		s = u.String()
	}
	return Clip(red.Text(s), MaxURL)
}

// Clip cuts s to at most max bytes at a UTF-8 boundary, marking a cut with
// an ellipsis inside the limit. Apply after redaction, never before.
func Clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	const mark = "…"
	cut := max - len(mark)
	if cut < 0 {
		cut = 0
	}
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + mark
}
