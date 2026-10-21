package keda

import (
	"regexp"
	"strconv"
	"strings"
)

// Version is a running KEDA version (§1.8): Known only when read from the
// handling operator's running image.
type Version struct {
	Known               bool
	Major, Minor, Patch int
}

func (v Version) String() string {
	if !v.Known {
		return "unknown"
	}
	return strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor) + "." + strconv.Itoa(v.Patch)
}

// AtLeast reports whether a known version is at least major.minor.
func (v Version) AtLeast(major, minor int) bool {
	return v.Known && (v.Major > major || v.Major == major && v.Minor >= minor)
}

// Before reports whether a known version is before major.minor.
func (v Version) Before(major, minor int) bool { return v.Known && !v.AtLeast(major, minor) }

var tagRe = regexp.MustCompile(`^v?(\d+)\.(\d+)(?:\.(\d+))?`)

// The repositories of KEDA's own images (ghcr.io, or Docker Hub).
var (
	operatorRepos = []string{"ghcr.io/kedacore/keda", "docker.io/kedacore/keda", "kedacore/keda", "index.docker.io/kedacore/keda"}
	metricsRepos  = []string{"ghcr.io/kedacore/keda-metrics-apiserver", "docker.io/kedacore/keda-metrics-apiserver", "kedacore/keda-metrics-apiserver", "index.docker.io/kedacore/keda-metrics-apiserver"}
)

// splitImage is an image reference's repository and tag, any digest dropped.
func splitImage(image string) (repo, tag string) {
	if i := strings.Index(image, "@"); i >= 0 {
		image = image[:i]
	}
	slash := strings.LastIndex(image, "/")
	if i := strings.LastIndex(image, ":"); i > slash {
		return image[:i], image[i+1:]
	}
	return image, ""
}

// IsOperatorImage reports whether an image is KEDA's own operator image.
func IsOperatorImage(image string) bool { return inRepos(image, operatorRepos) }

// IsMetricsServerImage reports whether an image is KEDA's own metrics
// server image (§2: the APIService's positive proof).
func IsMetricsServerImage(image string) bool { return inRepos(image, metricsRepos) }

func inRepos(image string, repos []string) bool {
	repo, _ := splitImage(image)
	for _, r := range repos {
		if repo == r {
			return true
		}
	}
	return false
}

// VersionOfImage reads the version from an official KEDA image's tag;
// any other image, or a tag that is not a version, is unknown.
func VersionOfImage(image string) Version {
	if !IsOperatorImage(image) {
		return Version{}
	}
	_, tag := splitImage(image)
	m := tagRe.FindStringSubmatch(tag)
	if m == nil {
		return Version{}
	}
	maj, _ := strconv.Atoi(m[1])
	min, _ := strconv.Atoi(m[2])
	patch := 0
	if m[3] != "" {
		patch, _ = strconv.Atoi(m[3])
	}
	return Version{Known: true, Major: maj, Minor: min, Patch: patch}
}

// PauseKind is what a scaler's pause annotations request.
type PauseKind int

const (
	PauseNone        PauseKind = iota
	PauseFull                  // the HPA deleted, the target held or frozen
	PauseDirectional           // the HPA kept, one direction off (2.18+)
)

// Intent is what the pause annotations request of a version (§1.3).
type Intent struct {
	Kind       PauseKind
	Annotation string // the annotation in effect for a full pause
	Requested  *int32 // paused-replicas, parsed
	Malformed  bool   // paused-replicas is not a 32-bit integer: KEDA fails its reconcile
	Directions []string
	// NotApplied names an annotation present that this known version does
	// not apply; Unverified, one whose effect depends on an unknown version.
	NotApplied string
	Unverified string
}

// boolAnnotation is KEDA's getBoolAnnotation: ParseBool, unparseable true.
func boolAnnotation(v *string) bool {
	if v == nil {
		return false
	}
	b, err := strconv.ParseBool(*v)
	return err != nil || b
}

// ScaledObjectIntent reads a ScaledObject's pause annotations (§1.3):
// paused-replicas wins by presence (2.7+); paused applies from 2.12, as a
// bool from 2.13 (before, any value paused); the directional pauses from
// 2.18.
func ScaledObjectIntent(p Pause, v Version) Intent {
	var in Intent
	if p.PausedReplicas != nil {
		in.Kind, in.Annotation = PauseFull, AnnPausedReplicas
		n, err := strconv.ParseInt(strings.TrimSpace(*p.PausedReplicas), 10, 32)
		if err != nil {
			in.Malformed = true
		} else {
			r := int32(n)
			in.Requested = &r
		}
	}
	if p.Paused != nil {
		switch {
		case v.Before(2, 12):
			in.NotApplied = AnnPaused
		case v.Known && v.Minor == 12 && v.Major == 2:
			if in.Kind == PauseNone {
				in.Kind, in.Annotation = PauseFull, AnnPaused // 2.12: any value paused
			}
		case v.Known:
			if boolAnnotation(p.Paused) && in.Kind == PauseNone {
				in.Kind, in.Annotation = PauseFull, AnnPaused
			}
		default: // unknown: true pauses on every version that applies it
			if boolAnnotation(p.Paused) {
				if in.Kind == PauseNone {
					in.Kind, in.Annotation = PauseFull, AnnPaused
				}
				in.Unverified = AnnPaused // before 2.12 it does nothing
			} else {
				in.Unverified = AnnPaused // before 2.13 "false" still paused
			}
		}
	}
	for _, d := range []struct {
		ann *string
		dir string
		key string
	}{{p.ScaleIn, "scale-in", AnnPausedScaleIn}, {p.ScaleOut, "scale-out", AnnPausedScaleOut}} {
		if !boolAnnotation(d.ann) {
			continue
		}
		switch {
		case v.Before(2, 18):
			if in.NotApplied == "" {
				in.NotApplied = d.key
			}
		case !v.Known:
			in.Directions = append(in.Directions, d.dir)
			if in.Unverified == "" {
				in.Unverified = d.key
			}
		default:
			in.Directions = append(in.Directions, d.dir)
		}
	}
	if in.Kind == PauseNone && len(in.Directions) > 0 {
		in.Kind = PauseDirectional
	}
	return in
}

// ScaledJobIntent reads a ScaledJob's pause annotation: paused applies from
// 2.11, as a bool from 2.13 (before, any value paused).
func ScaledJobIntent(p Pause, v Version) Intent {
	var in Intent
	if p.Paused == nil {
		return in
	}
	switch {
	case v.Before(2, 11):
		in.NotApplied = AnnPaused
	case v.Before(2, 13):
		in.Kind, in.Annotation = PauseFull, AnnPaused
	case v.Known:
		if boolAnnotation(p.Paused) {
			in.Kind, in.Annotation = PauseFull, AnnPaused
		}
	default:
		if boolAnnotation(p.Paused) {
			in.Kind, in.Annotation = PauseFull, AnnPaused
		}
		in.Unverified = AnnPaused
	}
	return in
}

// PauseCompleted is §1.3's positive evidence of a completed full pause of a
// ScaledObject, the same on every version: the condition KEDA writes once
// the pause is in place (Paused=True; on 2.10, which has none,
// pausedReplicaCount set for paused-replicas), Ready not False, and no HPA
// controlled by it in a current, complete read (hpaAbsent).
func PauseCompleted(so *ScaledObject, in Intent, hpaAbsent bool) bool {
	if in.Kind != PauseFull || in.Malformed || so.CondIs(CondReady, "False") || !hpaAbsent {
		return false
	}
	if so.CondIs(CondPaused, "True") {
		return true
	}
	return so.Cond(CondPaused) == nil && in.Annotation == AnnPausedReplicas && so.PausedCount != nil
}

// JobPauseCompleted is a ScaledJob's completed pause: it has no HPA, so
// Paused=True with Ready not False.
func JobPauseCompleted(j *ScaledJob, in Intent) bool {
	return in.Kind == PauseFull && j.CondIs(CondPaused, "True") && !j.CondIs(CondReady, "False")
}
