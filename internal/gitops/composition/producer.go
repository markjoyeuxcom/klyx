package composition

import "strings"

// Coverage is how much of the generators a join could read.
type Coverage int

const (
	CoverageComplete   Coverage = iota // every generator
	CoveragePartial                    // capped
	CoverageUnreadable                 // nothing readable
)

// Producer states (S1c §2, revisions 1 and 2). Only Verified, ByIdentity
// and Named attribute a generator's health to the artifact's producer
// block; none ever folds into the artifact's own verdict.
const (
	ProducerVerified   = "verified"     // the label's UID and the named generator agree
	ProducerByIdentity = "by identity"  // the label alone, found by UID
	ProducerNamed      = "named"        // a generator sourceRef alone: identity not recorded
	ProducerDeclared   = "declared"     // another kind: its health is not read
	ProducerDisagree   = "disagree"     // the named generator is not the recorded one
	ProducerGone       = "gone"         // a complete read finds no generator under the recorded UID
	ProducerNotFound   = "not found"    // the named generator is not in a complete read
	ProducerIncomplete = "incomplete"   // the generators' read is capped
	ProducerUnreadable = "not readable" // the generators could not be read
	ProducerNone       = "none"         // no producer recorded
)

// Producer is an artifact's producer, as its evidence establishes it.
type Producer struct {
	State string
	// Generator is the producer whose health may be shown.
	Generator *Generator
	// Declared is what sourceRef names.
	Declared *Ref
	// On disagree: Recorded is the live generator under the recorded UID,
	// if one was found; RecordedGone says a complete read found none;
	// otherwise the lookup's state is RecordedLookup.
	Recorded       *Generator
	RecordedGone   bool
	RecordedLookup string
}

// isGeneratorRef: sourceRef's kind is read as declared - only
// source.extensions.fluxcd.io ArtifactGenerator is a generator, never a
// same-named object of another kind.
func isGeneratorRef(r *Ref) bool {
	if r == nil || r.Kind != KindGenerator {
		return false
	}
	group, _, ok := strings.Cut(r.APIVersion, "/")
	return r.APIVersion == "" || (ok && group == GeneratorGroup)
}

func missingState(cov Coverage, complete string) string {
	switch cov {
	case CoverageUnreadable:
		return ProducerUnreadable
	case CoveragePartial:
		return ProducerIncomplete
	}
	return complete
}

// ProducerOf resolves an artifact's producer among the generators read.
func ProducerOf(a Artifact, gens []Generator, cov Coverage) Producer {
	ref := a.SourceRef
	if ref != nil && !isGeneratorRef(ref) {
		return Producer{State: ProducerDeclared, Declared: ref}
	}
	var byUID, byName *Generator
	for i := range gens {
		g := &gens[i]
		if a.GeneratorUID != "" && g.UID == a.GeneratorUID {
			byUID = g
		}
		if ref != nil && g.Namespace == ref.Namespace && g.Name == ref.Name {
			byName = g
		}
	}
	switch {
	case a.GeneratorUID != "" && ref != nil:
		if byName != nil && byName.UID == a.GeneratorUID {
			return Producer{State: ProducerVerified, Generator: byName, Declared: ref}
		}
		if byName == nil && byUID == nil {
			// neither the named nor the recorded generator is read
			return Producer{State: missingState(cov, ProducerGone), Declared: ref}
		}
		p := Producer{State: ProducerDisagree, Declared: ref, Recorded: byUID}
		if byUID == nil {
			p.RecordedGone = cov == CoverageComplete
			if !p.RecordedGone {
				p.RecordedLookup = missingState(cov, "")
			}
		}
		return p
	case a.GeneratorUID != "":
		if byUID != nil {
			return Producer{State: ProducerByIdentity, Generator: byUID}
		}
		return Producer{State: missingState(cov, ProducerGone)}
	case ref != nil:
		if byName != nil {
			return Producer{State: ProducerNamed, Generator: byName, Declared: ref}
		}
		return Producer{State: missingState(cov, ProducerNotFound), Declared: ref}
	}
	return Producer{State: ProducerNone}
}
