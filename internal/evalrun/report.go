package evalrun

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/moomora/klyx/internal/appbridge"
	"github.com/moomora/klyx/internal/evalrun/record"
)

// ScorerVersion changes whenever a status, a mark or a reason changes.
const ScorerVersion = "eval-1"

const (
	reportVersion = 1
	// MaxReportBytes bounds report.json (eval design §7).
	MaxReportBytes  = 2 << 20
	statementMax    = 1024
	missingTextMax  = 300
	notesPerFinding = 3
)

// Report is the eval's report (eval design §7). It holds no key, no config
// contents, no raw provider request or continuation and no raw tool
// result: Klyx's labelled findings and the record's IDs and flags only.
type Report struct {
	Version    int               `json:"version"`
	Scorer     string            `json:"scorer"`
	Started    time.Time         `json:"started"`
	Finished   time.Time         `json:"finished"`
	Provenance Provenance        `json:"provenance"`
	Summary    []ScenarioSummary `json:"summary"`
	Runs       []RunRecord       `json:"runs"`
	// Stopped says why the eval stopped before its last run
	Stopped string   `json:"stopped,omitempty"`
	Omitted []string `json:"omitted,omitempty"`
}

// Provenance is what produced the report.
type Provenance struct {
	Commit    string               `json:"commit"`
	Dirty     bool                 `json:"dirty"`
	Go        string               `json:"go"`
	Provider  string               `json:"provider,omitempty"`
	Model     string               `json:"model,omitempty"`
	Scenarios []ScenarioProvenance `json:"scenarios"`
}

// ScenarioProvenance pins a scenario's manifest and fixture.
type ScenarioProvenance struct {
	Name      string            `json:"name"`
	Manifest  string            `json:"manifestSha256"`
	AnchorUID string            `json:"anchorUID,omitempty"`
	Witnesses map[string]string `json:"witnesses,omitempty"`
}

// RunRecord is one repetition.
type RunRecord struct {
	Scenario    string            `json:"scenario"`
	Repetition  int               `json:"repetition"`
	Status      string            `json:"status"`
	Detail      string            `json:"detail,omitempty"`
	ReadyReason string            `json:"readyReason,omitempty"`
	Outcome     string            `json:"outcome,omitempty"`
	Provider    string            `json:"provider,omitempty"`
	Model       string            `json:"model,omitempty"`
	Mode        string            `json:"mode,omitempty"`
	Fallback    bool              `json:"fallbackToSnapshot,omitempty"`
	AnchorUID   string            `json:"anchorUID,omitempty"`
	Witnesses   map[string]string `json:"witnesses,omitempty"`
	Window      string            `json:"window,omitempty"`
	Record      *record.Facts     `json:"record,omitempty"`
	Marks       *Marks            `json:"marks,omitempty"`
	Findings    []FindingRow      `json:"findings,omitempty"`
	Missing     []MissingRow      `json:"missing,omitempty"`
	Calls       int               `json:"modelCalls"`
	Reads       int               `json:"reads"`
	Duration    string            `json:"duration"`
}

// FindingRow is one finding as Klyx labelled it, kept for adjudication.
type FindingRow struct {
	Type      string   `json:"type"`
	Label     string   `json:"label"`
	Statement string   `json:"statement"`
	Subject   string   `json:"subject"`
	Cites     []string `json:"cites,omitempty"`
	Notes     []string `json:"notes,omitempty"`
	// Adjudication is the owner's: right, wrong or partial (eval design
	// §6). The runner leaves it empty.
	Adjudication string `json:"adjudication"`
}

// MissingRow is evidence the turn did not read, Klyx's or the model's.
type MissingRow struct {
	From string `json:"from"`
	Read string `json:"read,omitempty"`
	Text string `json:"text"`
}

// ScenarioSummary is a scenario's denominators and mark rates.
type ScenarioSummary struct {
	Scenario  string         `json:"scenario"`
	Requested int            `json:"requested"`
	Statuses  map[string]int `json:"statuses"`
	// Completed is the denominator of every mark; zero means no result.
	Completed      int    `json:"completed"`
	Result         string `json:"result"`
	Grounded       int    `json:"groundedCause"`
	Lookalike      int    `json:"citedLookalike"`
	Unsupported    int    `json:"unsupportedCauses"`
	EmptyReports   int    `json:"explicitEmptyReports"`
	ControlPasses  *int   `json:"controlPasses,omitempty"`
	ProxyCause     int    `json:"proxyCause"`
	ProxyLookalike int    `json:"proxyLookalike"`
	Adjudication   string `json:"adjudication"`
}

func summarize(s *Scenario, requested int, all []RunRecord) ScenarioSummary {
	sum := ScenarioSummary{Scenario: s.Name, Requested: requested, Statuses: map[string]int{}, Adjudication: "unadjudicated"}
	for _, st := range Statuses {
		sum.Statuses[st] = 0
	}
	passes := 0
	for _, r := range all {
		if r.Scenario != s.Name {
			continue
		}
		sum.Statuses[r.Status]++
		if r.Marks == nil {
			continue
		}
		sum.Completed++
		m := r.Marks
		for _, b := range []struct {
			on bool
			n  *int
		}{{m.GroundedCause, &sum.Grounded}, {m.CitedLookalike, &sum.Lookalike}, {m.EmptyReport, &sum.EmptyReports}} {
			if b.on {
				*b.n++
			}
		}
		if m.UnsupportedCauses > 0 {
			sum.Unsupported++
		}
		if m.ProxyCause > 0 {
			sum.ProxyCause++
		}
		if m.ProxyLookalike > 0 {
			sum.ProxyLookalike++
		}
		if m.Control != nil && *m.Control {
			passes++
		}
	}
	if s.Control {
		sum.ControlPasses = &passes
	}
	if sum.Completed == 0 {
		sum.Result = "no result: no completed run"
	} else {
		sum.Result = fmt.Sprintf("%d of %d requested runs completed", sum.Completed, requested)
	}
	return sum
}

func findingRows(f *appbridge.FindingsDTO) []FindingRow {
	var out []FindingRow
	for _, fd := range f.Findings {
		row := FindingRow{Type: fd.Type, Label: fd.Label, Statement: clip(fd.Statement, statementMax),
			Subject: strings.TrimSpace(fd.Subject.Kind + " " + strings.TrimPrefix(fd.Subject.Namespace+"/"+fd.Subject.Name, "/")),
			Cites:   fd.Cites}
		for i, n := range fd.Notes {
			if i == notesPerFinding {
				row.Notes = append(row.Notes, fmt.Sprintf("+%d more", len(fd.Notes)-i))
				break
			}
			row.Notes = append(row.Notes, clip(n, missingTextMax))
		}
		out = append(out, row)
	}
	return out
}

func missingRows(f *appbridge.FindingsDTO) []MissingRow {
	var out []MissingRow
	for _, m := range f.Missing {
		out = append(out, MissingRow{From: m.From, Read: m.Read, Text: clip(m.Text, missingTextMax)})
	}
	return out
}

func clip(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max]) + "…"
}

// BuildProvenance is the binary's commit and whether its tree was clean.
func BuildProvenance() Provenance {
	p := Provenance{Commit: "unknown"}
	if bi, ok := debug.ReadBuildInfo(); ok {
		p.Go = bi.GoVersion
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				p.Commit = s.Value
			case "vcs.modified":
				p.Dirty = s.Value == "true"
			}
		}
	}
	return p
}

// encode bounds the report: when it passes MaxReportBytes it drops, in
// order, missing evidence, then long statements, then findings from the
// last runs, and says what it left out.
func (r *Report) encode() ([]byte, error) {
	steps := []func() string{
		func() string {
			for i := range r.Runs {
				r.Runs[i].Missing = nil
			}
			return "every run's missing evidence"
		},
		func() string {
			for i := range r.Runs {
				for j := range r.Runs[i].Findings {
					r.Runs[i].Findings[j].Statement = clip(r.Runs[i].Findings[j].Statement, 200)
				}
			}
			return "statements past 200 characters"
		},
	}
	for {
		b, err := json.MarshalIndent(r, "", "  ")
		if err != nil {
			return nil, err
		}
		if len(b) <= MaxReportBytes {
			return b, nil
		}
		if len(steps) > 0 {
			r.Omitted = append(r.Omitted, steps[0]())
			steps = steps[1:]
			continue
		}
		dropped := false
		for i := len(r.Runs) - 1; i >= 0; i-- {
			if len(r.Runs[i].Findings) > 0 {
				r.Runs[i].Findings = nil
				r.Omitted = append(r.Omitted, fmt.Sprintf("the findings of %s run %d", r.Runs[i].Scenario, r.Runs[i].Repetition))
				dropped = true
				break
			}
		}
		if !dropped {
			return b, fmt.Errorf("the report is %d bytes even without findings", len(b))
		}
	}
}

// Write writes report.json and report.md into dir, private to the owner.
func (r *Report) Write(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := r.encode()
	if err != nil {
		return err
	}
	if err := writePrivate(filepath.Join(dir, "report.json"), b); err != nil {
		return err
	}
	return writePrivate(filepath.Join(dir, "report.md"), []byte(r.Markdown()))
}

func writePrivate(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Markdown is the report for reading and adjudicating.
func (r *Report) Markdown() string {
	var b strings.Builder
	p := r.Provenance
	fmt.Fprintf(&b, "# Klyx AI eval report\n\n")
	fmt.Fprintf(&b, "Scorer %s, build %s%s, %s. Provider %s, model %s.\n\n", r.Scorer, short(p.Commit), map[bool]string{true: " (dirty)", false: ""}[p.Dirty], p.Go, orNone(p.Provider), orNone(p.Model))
	fmt.Fprintf(&b, "The marks verify citations, not diagnoses. Diagnosis accuracy exists only once the owner has adjudicated the cause statements below.\n\n")
	if r.Stopped != "" {
		fmt.Fprintf(&b, "**%s.** The runs after it were not made.\n\n", r.Stopped)
	}
	b.WriteString("| Scenario | Requested | Completed | Statuses | Grounded cause | Cited a lookalike | Unsupported causes | Control | Proxy: cause | Proxy: lookalike |\n|---|---|---|---|---|---|---|---|---|---|\n")
	for _, s := range r.Summary {
		var st []string
		keys := make([]string, 0, len(s.Statuses))
		for k := range s.Statuses {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if n := s.Statuses[k]; n > 0 {
				st = append(st, fmt.Sprintf("%s %d", k, n))
			}
		}
		control := "-"
		if s.ControlPasses != nil {
			control = fmt.Sprintf("%d/%d", *s.ControlPasses, s.Completed)
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %s | %s | %s | %s | %s | %s | %s |\n", s.Scenario, s.Requested, s.Completed, strings.Join(st, ", "),
			rate(s.Grounded, s.Completed), rate(s.Lookalike, s.Completed), rate(s.Unsupported, s.Completed), control,
			rate(s.ProxyCause, s.Completed), rate(s.ProxyLookalike, s.Completed))
	}
	b.WriteString("\n## Runs\n")
	for _, run := range r.Runs {
		fmt.Fprintf(&b, "\n### %s, run %d: %s\n\n", run.Scenario, run.Repetition, run.Status)
		if run.Detail != "" {
			fmt.Fprintf(&b, "%s\n\n", run.Detail)
		}
		if run.Mode != "" {
			fmt.Fprintf(&b, "Mode %s%s; %d model calls, %d reads, %s.\n\n", run.Mode, map[bool]string{true: " (fell back from tools)", false: ""}[run.Fallback], run.Calls, run.Reads, run.Duration)
		}
		if run.Record != nil {
			for _, o := range run.Record.Predicates {
				if o.Established {
					fmt.Fprintf(&b, "- planted evidence %s: delivered in %s\n", o.Predicate, o.Read)
				} else {
					fmt.Fprintf(&b, "- planted evidence %s: not established (%s)\n", o.Predicate, o.Reason)
				}
			}
		}
		for _, f := range run.Findings {
			if f.Type != typeCause {
				continue
			}
			fmt.Fprintf(&b, "- [ ] cause, %s, about %s, citing %s: %s\n", f.Label, f.Subject, strings.Join(f.Cites, ", "), f.Statement)
		}
	}
	if len(r.Omitted) > 0 {
		fmt.Fprintf(&b, "\nLeft out to fit %d bytes: %s.\n", MaxReportBytes, strings.Join(r.Omitted, "; "))
	}
	return b.String()
}

func rate(n, of int) string {
	if of == 0 {
		return "no result"
	}
	return fmt.Sprintf("%d/%d", n, of)
}

func short(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
