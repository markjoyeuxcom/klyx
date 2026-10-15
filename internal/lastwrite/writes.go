// Package lastwrite reads an object's last recorded managed-field update (The
// Line parent spec §8.3, slice 7 note §2): from metadata.managedFields, the
// entry with the latest time - when, by which manager, with which operation
// and subresource. It is not an audit log: a manager name is a workflow
// identifier, not an authenticated writer, and Klyx never says which field
// changed.
package lastwrite

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Record is one managed-field entry as Klyx shows it.
type Record struct {
	Time        time.Time
	Manager     string
	Operation   string // Apply | Update
	Subresource string // "" for the object itself; "status" for a status update
}

// StatusUpdate: a status-subresource entry, labelled "status update" and
// never used for change proximity.
func (r Record) StatusUpdate() bool { return r.Subresource == "status" }

// Writes is what the managed fields record. Both nil: "no managed-field
// record" (absent, reset, or no entry carries a time).
type Writes struct {
	Last     *Record // the latest entry, a status update included
	LastSpec *Record // the latest entry that is not a status update: the one change proximity may use
}

// Read picks the latest entries. An entry with no time is skipped; ties keep
// the first in the list.
func Read(entries []metav1.ManagedFieldsEntry) Writes {
	var w Writes
	for _, e := range entries {
		if e.Time == nil || e.Time.IsZero() {
			continue
		}
		r := Record{Time: e.Time.UTC(), Manager: e.Manager, Operation: string(e.Operation), Subresource: e.Subresource}
		if w.Last == nil || r.Time.After(w.Last.Time) {
			rc := r
			w.Last = &rc
		}
		if !r.StatusUpdate() && (w.LastSpec == nil || r.Time.After(w.LastSpec.Time)) {
			rc := r
			w.LastSpec = &rc
		}
	}
	return w
}
