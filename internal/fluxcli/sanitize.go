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

// DiffWithheld replaces the body of a payload kind's block.
const DiffWithheld = "  (changes withheld: this kind carries embedded payloads)"

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
		if !withholding {
			kept = append(kept, urlscrub.Text(l).Text)
		}
	}
	return strings.Join(kept, "\n")
}

// protectedSubject reports a kind whose values Klyx protects: a payload kind
// in any group, or a Secret.
func protectedSubject(kind string) bool {
	return kind == "Secret" || crd.IsPayloadKindName(kind)
}

// sanitizeError keeps error text unless it names a protected subject. Error
// text can run over several lines and echo submitted values (a validation or
// admission refusal), so then the whole text is replaced by each protected
// subject's dry-run failure and reason, and nothing else.
func sanitizeError(text string) string {
	if text == "" {
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
		return urlscrub.Text(text).Text
	}
	reasons := map[string]string{}
	for _, m := range dryRun.FindAllStringSubmatch(plain, -1) {
		reasons[m[1]] = m[2]
	}
	out := make([]string, 0, len(named)+1)
	for _, n := range named {
		out = append(out, fmt.Sprintf("%s dry-run failed%s", n, reasons[n]))
	}
	out = append(out, ErrorWithheld+"; other errors in this run are withheld with it")
	return strings.Join(out, "\n")
}
