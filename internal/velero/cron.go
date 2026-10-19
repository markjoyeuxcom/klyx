package velero

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	gocron "github.com/netresearch/go-cron"
	robfig "github.com/robfig/cron/v3"

	"github.com/moomora/klyx/internal/urlscrub"
)

// The cron libraries Velero has parsed schedules with (coverage S2a §1):
// robfig/cron v3 for v1.15.0 to v1.18.2, netresearch/go-cron for v1.18.3
// and later. Both use ParseStandard; their semantics differ for a few
// forms (day of month with day of week, day-of-week 7, @manual, the
// spring-forward hour), so the due time is computed with the library the
// server runs, or with both when its version is unresolved.
const (
	LibraryRobfig = "robfig/cron v3"
	LibraryGoCron = "go-cron"
)

// maxCron is go-cron's spec limit; Klyx reads no longer cron.
const maxCron = 1024

// Version is a Velero server version read from its image tag.
type Version struct{ Major, Minor, Patch int }

func (v Version) less(o Version) bool {
	if v.Major != o.Major {
		return v.Major < o.Major
	}
	if v.Minor != o.Minor {
		return v.Minor < o.Minor
	}
	return v.Patch < o.Patch
}

func (v Version) String() string { return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch) }

var (
	firstSupported = Version{1, 15, 0}
	goCronSince    = Version{1, 18, 3}
)

// imageVersion matches a Velero server image's tag: any registry and
// repository path ending in velero, a tag v<major>.<minor>.<patch>, and an
// optional pre-release, build or digest suffix.
var imageVersion = regexp.MustCompile(`^(?:[^@]*/)?velero:v(\d+)\.(\d+)\.(\d+)(?:[-+][0-9A-Za-z.-]*)?(?:@sha256:[0-9a-f]{64})?$`)

// VersionOfImage reads the server version from its container image; ok is
// false for a digest-only image or a tag that is not a release.
func VersionOfImage(image string) (Version, bool) {
	m := imageVersion.FindStringSubmatch(image)
	if m == nil {
		return Version{}, false
	}
	var v [3]int
	for i := range v {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return Version{}, false
		}
		v[i] = n
	}
	return Version{v[0], v[1], v[2]}, true
}

// Due is Velero's own next run for a schedule: D = Next(base), base being
// lastBackup, else the creation time, replaced by lastSkipped when later
// (schedule_controller.go 275-289).
type Due struct {
	D time.Time
	// Zero: the cron never matches a time (a never-satisfiable spec, or
	// @manual on go-cron). Velero treats it as always due.
	Zero bool
	// Unknown is why D cannot be computed; empty when it can.
	Unknown string
	// Library names the semantics used: one library, or "both agree".
	Library string
	// Zone is the time zone the spec was read in.
	Zone string
}

// Server is what the due time needs from the Velero server of the
// schedule's namespace. A fact that is not resolved (a rollout in
// progress, a TZ from valueFrom, two servers that disagree) leaves the
// states that need it unknown, never a fault.
type Server struct {
	Version         Version
	VersionResolved bool
	// Zone is the server's IANA time zone (its literal TZ, else UTC);
	// ZoneResolved is false when it cannot be read.
	Zone         string
	ZoneResolved bool
}

// Base is the time Velero computes its next run from.
func Base(lastBackup, created, lastSkipped time.Time) time.Time {
	base := lastBackup
	if base.IsZero() {
		base = created
	}
	if lastSkipped.After(base) {
		base = lastSkipped
	}
	return base
}

func hasZonePrefix(spec string) bool {
	return strings.HasPrefix(spec, "TZ=") || strings.HasPrefix(spec, "CRON_TZ=")
}

// nextWith parses spec with one library and returns Next(base); a robfig
// parser panic (a zone prefix with no space) is a parse error, as Velero's
// recover makes it.
func nextWith(lib, spec string, base time.Time) (next time.Time, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	switch lib {
	case LibraryRobfig:
		s, err := robfig.ParseStandard(spec)
		if err != nil {
			return time.Time{}, err
		}
		return s.Next(base), nil
	default:
		s, err := gocron.ParseStandard(spec)
		if err != nil {
			return time.Time{}, err
		}
		return s.Next(base), nil
	}
}

// ParseError is the parse error of spec under the library the server uses,
// or both libraries' errors when its version is unresolved: Klyx's own
// account of a FailedValidation, never Velero's text (S2a §6).
func ParseError(spec string, srv Server) string {
	if spec == "" {
		return "the cron is empty"
	}
	if len(spec) > maxCron {
		return "the cron is too long to read"
	}
	var errs []string
	for _, lib := range librariesFor(srv) {
		if _, err := nextWith(lib, spec, time.Unix(0, 0).UTC()); err != nil {
			// a parser echoes the spec: the URL rule, as for any display string
			errs = append(errs, lib+": "+urlscrub.Field(err.Error()).Text)
		}
	}
	return strings.Join(errs, "; ")
}

// librariesFor is the library a resolved version uses, else both.
func librariesFor(srv Server) []string {
	if !srv.VersionResolved {
		return []string{LibraryRobfig, LibraryGoCron}
	}
	if srv.Version.less(goCronSince) {
		return []string{LibraryRobfig}
	}
	return []string{LibraryGoCron}
}

// NextRun computes Velero's own due time for spec from base.
//
// With no zone prefix the libraries read the cron in the zone of the time
// passed to Next (Velero passes metav1 times in the pod's local zone), so
// base is moved into the server's zone first. When the version is
// unresolved Klyx computes both libraries; D stands only when both parse
// and agree (S2a design revision 1: an Enabled phase never names the
// library).
func NextRun(spec string, base time.Time, srv Server) Due {
	if len(spec) > maxCron {
		return Due{Unknown: "the cron is too long to read"}
	}
	zone := "the cron's own zone"
	if !hasZonePrefix(spec) {
		if !srv.ZoneResolved {
			return Due{Unknown: "the server's time zone is unresolved"}
		}
		name := srv.Zone
		if name == "" {
			name = "UTC"
		}
		loc, err := time.LoadLocation(name)
		if err != nil {
			// fixed wording: the raw value is the server's TZ, free text
			// (Codex's S2a implementation review 2, finding 1)
			return Due{Unknown: "Klyx does not know the server's time zone"}
		}
		base, zone = base.In(loc), name
	}
	if srv.VersionResolved && srv.Version.less(firstSupported) {
		return Due{Unknown: "Velero before 1.15 uses another cron library", Zone: zone}
	}
	libs := librariesFor(srv)
	var ds []time.Time
	for _, lib := range libs {
		d, err := nextWith(lib, spec, base)
		if err != nil {
			if len(libs) == 1 {
				return Due{Unknown: "Klyx cannot parse this cron: " + urlscrub.Field(err.Error()).Text, Library: lib, Zone: zone}
			}
			return Due{Unknown: "its next run depends on Velero's version, which Klyx could not resolve", Zone: zone}
		}
		ds = append(ds, d)
	}
	lib := libs[0]
	if len(ds) == 2 {
		if !ds[0].Equal(ds[1]) {
			return Due{Unknown: "its next run depends on Velero's version, which Klyx could not resolve", Zone: zone}
		}
		lib = "both agree"
	}
	return Due{D: ds[0], Zero: ds[0].IsZero(), Library: lib, Zone: zone}
}
