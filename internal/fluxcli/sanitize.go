package fluxcli

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/moomora/klyx/internal/crd"
	"github.com/moomora/klyx/internal/urlscrub"
)

// Coverage S0b for `flux diff` (found while fixing Codex's S0b implementation
// review, finding 3; the outbound-path matrix had missed this path). flux
// prints, per built object, a header and then the changed fields of the live
// and merged objects, values included, so a registered payload kind's block
// would carry HelmRelease values, ResourceSet templates or patches. Its body
// is withheld; every other line passes the URL rule. flux masks Secret
// values itself.

// DiffWithheld replaces the body of a payload kind's block, and everything
// after it: flux prints field paths and values as text, so a map key can
// carry a line that looks like a block header, and no later header can be
// trusted to end the withholding (Codex's re-review, finding 2).
const DiffWithheld = "  (changes withheld: this kind carries embedded payloads, and flux's text cannot be split safely after it, so the rest of the diff is withheld too)"

// ErrorWithheld replaces error text that names a protected subject.
const ErrorWithheld = "its message is withheld because this kind carries protected values"

var (
	ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)
	// header is a block header: "► Kind/ns/name drifted", "📁 Kind/ns/name
	// changed", "⚠️ Kind/ns/name skipped: …". Kind is the first segment.
	header = regexp.MustCompile(`^\s*(►|📁|⚠️?)\s+([A-Za-z][A-Za-z0-9]*)/`)
	// subject is a printed Kind/ns/name (or Kind/name) anywhere in a line.
	subject = regexp.MustCompile(`\b([A-Z][A-Za-z0-9]*)/[a-z0-9]([-a-z0-9.]*[a-z0-9])?(/[a-z0-9]([-a-z0-9.]*[a-z0-9])?)?`)
	dryRun  = regexp.MustCompile(`([A-Z][A-Za-z0-9]*/\S+) dry-run failed( \([^)]*\))?`)
)

// sanitizeDiff withholds payload kinds' blocks and applies the URL rule.
func sanitizeDiff(out string) string {
	lines := strings.Split(out, "\n")
	kept := make([]string, 0, len(lines))
	withholding := false
	for _, l := range lines {
		if withholding {
			break // sticky: nothing after a payload block is trusted
		}
		if m := header.FindStringSubmatch(ansi.ReplaceAllString(l, "")); m != nil {
			// A recursive Kustomization's "📁" header introduces its children's
			// own blocks, so it never withholds what follows.
			withholding = m[1] == "►" && crd.IsPayloadKindName(m[2])
			kept = append(kept, urlscrub.Text(l).Text)
			if withholding {
				kept = append(kept, DiffWithheld)
			}
			continue
		}
		kept = append(kept, urlscrub.Text(l).Text)
	}
	return strings.Join(kept, "\n")
}

// protectedSubject reports a kind whose values Klyx protects: a payload kind
// in any group, or a Secret.
func protectedSubject(kind string) bool {
	return kind == "Secret" || crd.IsPayloadKindName(kind)
}

// UnclassifiedWithheld replaces flux's error text when nothing in it can be
// shown safely (Codex's re-review, finding 3).
const UnclassifiedWithheld = "flux's message is withheld: its build and dry-run errors can echo patches, values or templates. Run the diff in a terminal to read it."

// sanitizeError never forwards flux's free text: a build error can echo an
// inline patch before any object is named, so the absence of a protected
// subject is no evidence of safety. A protected subject's dry-run failure
// keeps its subject and reason; anything else is withheld whole.
func sanitizeError(text string) string {
	if strings.TrimSpace(text) == "" {
		return text
	}
	plain := ansi.ReplaceAllString(text, "")
	var named []string
	seen := map[string]bool{}
	for _, m := range subject.FindAllStringSubmatch(plain, -1) {
		if protectedSubject(m[1]) && !seen[m[0]] {
			seen[m[0]] = true
			named = append(named, m[0])
		}
	}
	if len(named) == 0 {
		return UnclassifiedWithheld
	}
	reasons := map[string]string{}
	for _, m := range dryRun.FindAllStringSubmatch(plain, -1) {
		reasons[m[1]] = m[2]
	}
	out := make([]string, 0, len(named)+1)
	for _, n := range named {
		out = append(out, fmt.Sprintf("%s dry-run failed%s", n, reasons[n]))
	}
	out = append(out, ErrorWithheld+"; other errors in this run are withheld with it. "+UnclassifiedWithheld)
	return strings.Join(out, "\n")
}
