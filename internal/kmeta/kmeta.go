// Package kmeta reads the object metadata that dates writes, so a rule can
// tell whether a condition's lastTransitionTime still dates the role it
// supports (The Line §8.1).
package kmeta

import (
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Writes are an object's latest managedFields times: Main for the main
// resource (spec and metadata), Status for the status subresource. Known is
// false when the object carries no managedFields entry with a time, and then
// neither time proves anything.
type Writes struct {
	Main, Status time.Time
	Known        bool
}

// WritesOf reads managedFields. Second granularity, as the API server
// records it.
func WritesOf(u *unstructured.Unstructured) Writes {
	var w Writes
	for _, e := range u.GetManagedFields() {
		if e.Time == nil {
			continue
		}
		t := e.Time.Time
		w.Known = true
		switch e.Subresource {
		case "":
			if t.After(w.Main) {
				w.Main = t
			}
		case "status":
			if t.After(w.Status) {
				w.Status = t
			}
		}
	}
	return w
}

// Undisturbed reports that no generation overlay - a spec change the
// controller had not yet observed, which a rule shows as syncing or last
// observed - can have interrupted a role since at, the transition time of the
// condition it was read from. An overlay that ends leaves no timestamp: the
// controller re-observes the new generation without changing the condition's
// status. Two cases prove there was none:
//   - generation 1: the spec never changed, so every condition was written
//     for the generation the object has now;
//   - managedFields show no main-resource write after at (so the generation
//     has not changed since) and no status write after at either (so the
//     condition's observedGeneration has not changed since). The object is
//     not stale now, so it was not stale at any time since at.
//
// A same-second write is treated as after at: the order is unknowable.
func Undisturbed(generation int64, w Writes, at time.Time) bool {
	if at.IsZero() {
		return false
	}
	if generation == 1 {
		return true
	}
	return w.Known && w.Main.Before(at) && !w.Status.After(at)
}

// SpecSettled reports that the spec (and so every spec input a rule reads)
// has not changed since at: generation 1, or no main-resource write at or
// after at. Status writes are not considered.
func SpecSettled(generation int64, w Writes, at time.Time) bool {
	if at.IsZero() {
		return false
	}
	if generation == 1 {
		return true
	}
	return w.Known && w.Main.Before(at)
}
