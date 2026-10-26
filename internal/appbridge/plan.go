package appbridge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"
)

// Settings slice F §5: a change that affects running work is applied as a
// plan from what runs to a validated candidate. The person confirms the
// plan, not the form: the confirmation binds to its digest, which covers
// the changes, what the change ends and what continues, the interlock, the
// file version and the runtime revisions it was planned from. Apply
// re-plans from what runs then, and refuses with the new plan when the
// digest differs.

// Plan kinds: what the candidate is.
const (
	planSave   = "save"   // the person's draft
	planFile   = "file"   // "Use the file's values": the file already says so
	planRevert = "revert" // back to the last active terms
)

// PlanDTO is one activation plan.
type PlanDTO struct {
	Section string `json:"section"` // ai | notifications | git | fleet
	Kind    string `json:"kind"`    // save | file | revert | connect | remove | retry
	// Cluster and Class: a fleet plan's cluster, and what its Apply does
	// (Settings S2 §4.2)
	Cluster     string            `json:"cluster,omitempty"`
	Class       string            `json:"class,omitempty"`
	Digest      string            `json:"digest"`
	FileVersion string            `json:"fileVersion"`
	Runtime     map[string]string `json:"runtime"`
	Changes     []PlanChangeDTO   `json:"changes"`
	Ends        []PlanWorkDTO     `json:"ends"`
	Continues   []PlanWorkDTO     `json:"continues"`
	Interlock   *InterlockDTO     `json:"interlock,omitempty"`
	// Writes: the Apply writes fleet.yaml; false for "Use the file's
	// values", which only checks the file is still the version planned from
	Writes bool `json:"writes"`
	// Activates: the Apply builds and publishes what it plans - the
	// revision moves - even when nothing differs from the settings shown
	// as in use (a saved block whose activation failed: F2a review, R5)
	Activates bool `json:"activates"`
	// Refused: a candidate that cannot be applied, and why; nothing is
	// written
	Refused string `json:"refused,omitempty"`
	// Records: a removal's keychain records, each its own choice (S2 §4.4)
	Records []PlanRecordDTO `json:"records,omitempty"`
}

// PlanChangeDTO is one effective change, by registry id, as shown: never a
// secret, only its state.
type PlanChangeDTO struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	From  string `json:"from"`
	To    string `json:"to"`
}

// PlanWorkDTO is one piece of running work a plan ends or continues, by
// identity: "an investigation on aks-we-prd, started 10:42".
type PlanWorkDTO struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"` // chat | investigation | turn | offer | step | explain | suggest-fix | fetch
	Cluster string `json:"cluster"`
	Started string `json:"started,omitempty"` // RFC 3339
	What    string `json:"what"`
}

// InterlockDTO is the typed confirmation a plan needs, and why.
type InterlockDTO struct {
	Phrase string `json:"phrase"`
	Why    string `json:"why"`
}

// ApplyResultDTO is an Apply's outcome: applied; refused with the plan it
// was replaced by, which never inherits the confirmation; a quiet Apply's
// plan, returned unapplied for review; or saved, not active, with the
// reason.
type ApplyResultDTO struct {
	OK             bool     `json:"ok"`
	Error          string   `json:"error,omitempty"`
	Plan           *PlanDTO `json:"plan,omitempty"`
	SavedNotActive string   `json:"savedNotActive,omitempty"`
	// Note: applied, with something to know (the OS has notifications off
	// for Klyx)
	Note string `json:"note,omitempty"`
}

// seal sets the plan's digest over everything in it the person reviews,
// and bind: what the candidate is beyond what the plan shows (never a
// secret).
func seal(p *PlanDTO, bind string) {
	p.Digest = ""
	b, err := json.Marshal(struct {
		Plan *PlanDTO `json:"plan"`
		Bind string   `json:"bind"`
	}{p, bind})
	if err != nil {
		panic(err) // the plan is plain data
	}
	sum := sha256.Sum256(b)
	p.Digest = hex.EncodeToString(sum[:])
}

// workVersion is a work list's version: the identities in it, so a turn,
// an investigation or an offer starting or ending changes it.
func workVersion(work []PlanWorkDTO) string {
	h := sha256.New()
	for _, w := range work {
		h.Write([]byte(w.ID))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// The candidate cache (Settings slice F §5): bounded by entries and bytes,
// expiring, consumed by a successful Apply. It holds no secret.
const (
	planCacheEntries = 16
	planCacheBytes   = 1 << 20
	planCacheTTL     = 5 * time.Minute
)

type planCache struct {
	mu      sync.Mutex
	now     func() time.Time
	entries map[string]*planEntry
	order   []string // oldest first
	size    int
}

type planEntry struct {
	value any
	size  int
	at    time.Time
}

func newPlanCache(now func() time.Time) *planCache {
	return &planCache{now: now, entries: map[string]*planEntry{}}
}

// put keeps value under digest, sized by its encoding; the oldest entries
// go first when a bound is reached. A value larger than the whole budget is
// not kept.
func (c *planCache) put(digest string, value any) {
	b, err := json.Marshal(value)
	if err != nil {
		return
	}
	size := len(b) + len(digest)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dropLocked(digest)
	if size > planCacheBytes {
		return
	}
	for len(c.order) > 0 && (len(c.order) >= planCacheEntries || c.size+size > planCacheBytes) {
		c.dropLocked(c.order[0])
	}
	c.entries[digest] = &planEntry{value: value, size: size, at: c.now()}
	c.order = append(c.order, digest)
	c.size += size
}

// get is digest's value while it has not expired.
func (c *planCache) get(digest string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[digest]
	if !ok {
		return nil, false
	}
	if c.now().Sub(e.at) >= planCacheTTL {
		c.dropLocked(digest)
		return nil, false
	}
	return e.value, true
}

// consume removes digest's value: a successful Apply's.
func (c *planCache) consume(digest string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dropLocked(digest)
}

func (c *planCache) dropLocked(digest string) {
	e, ok := c.entries[digest]
	if !ok {
		return
	}
	delete(c.entries, digest)
	c.size -= e.size
	for i, d := range c.order {
		if d == digest {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
}
