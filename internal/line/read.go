package line

// The station read truth table (slice 3 note §2.4).

// unitKinds are the kinds that define units: always needed.
var unitKinds = []string{"kustomizations", "helmreleases", "applications"}

// kustomizationSourceKinds are the source kinds a Kustomization can
// reference, all needed while the Kustomization kind is unread (its
// references are unknown, never assumed empty).
var kustomizationSourceKinds = []string{"gitrepositories", "ocirepositories", "buckets"}

// ReadOfCoverage maps an Observer coverage word to a kind read.
func ReadOfCoverage(coverage string) string {
	switch coverage {
	case "live":
		return ReadOK
	case "absent":
		return KindAbsent
	case "stale":
		return ReadStale
	case "unreadable":
		return ReadError
	}
	return ReadChecking
}

// Availability is what gates a station before its kinds are read.
type Availability struct {
	Usable    bool // the connection word allows observation
	Gapped    bool // a needed part has a loss awaiting its after-gap
	Attaching bool // the service has not yet bootstrapped this incarnation
}

// ResolveRead fills in.KindRead from the Observer's per-kind coverage and
// returns the station read. Needed kinds are the unit kinds plus the source
// kinds the Git units (Kustomizations) reference; when the Kustomization kind
// is not read, every Git-capable Flux source kind is needed. A HelmRelease's
// source is never needed: its recorded-name chart unit compares history
// alone, and the source is context (Unit.ContextRead).
func ResolveRead(in *StationInput, coverage map[string]string, av Availability) string {
	in.KindRead = map[string]string{}
	for kind, cov := range coverage {
		in.KindRead[kind] = ReadOfCoverage(cov)
	}
	if !av.Usable || av.Gapped || av.Attaching {
		return ReadChecking
	}
	needed := map[string]bool{}
	for _, k := range unitKinds {
		needed[k] = true
	}
	if r := in.kindRead("kustomizations"); r != ReadOK && r != KindAbsent {
		for _, k := range kustomizationSourceKinds {
			needed[k] = true
		}
	}
	for _, o := range in.Kustomizations {
		if k := sourceKinds[o.Data.SourceKind]; k != "" {
			needed[k] = true
		}
	}
	live, bad, worst := false, false, ReadOK
	for k := range needed {
		r := in.kindRead(k)
		switch r {
		case ReadOK:
			live = true
		case KindAbsent:
		default:
			bad = true
			worst = worseRead(worst, r)
		}
	}
	switch {
	case !bad:
		return ReadOK
	case live:
		return ReadPartial
	}
	return worst
}
