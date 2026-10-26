// Package endpointcred resolves the credential of an external endpoint -
// metrics, Alertmanager, Loki - from the OS keychain, bound to the origin it
// was stored for, and tracks each endpoint's credential revision so a
// change reaches every client at once (coverage C5 §3, §7).
package endpointcred

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/safehttp"
	"github.com/moomora/klyx/internal/secretstore"
)

// Account is the keychain account an endpoint's credential is stored under.
func Account(cluster, kind string) string { return "endpoint/" + cluster + "/" + kind }

// recordVersion is the stored record's version.
const recordVersion = 1

// Record is the stored value: the secret, its type, and the canonical
// origin it was stored for (C5 §3).
type Record struct {
	V      int    `json:"v"`
	Origin string `json:"origin"`
	Type   string `json:"type"` // bearer | basic
	Secret string `json:"secret"`
}

// Encode is the record as the keychain holds it.
func (r Record) Encode() (string, error) {
	b, err := json.Marshal(r)
	return string(b), err
}

// State is a credential's state as Settings shows it (C5 §3).
type State string

const (
	NotNeeded   State = "not needed"           // no auth configured
	NotSent     State = "not sent"             // auth on a proxied or discovered endpoint
	Stored      State = "stored"               // in the keychain, for this origin
	Missing     State = "missing"              // auth configured, nothing stored
	OtherOrigin State = "for another origin"   // stored for a different origin
	Unusable    State = "unusable record"      // stored, but not a record Klyx can use
	Unavailable State = "keychain unavailable" // locked, denied, or no store
	Revoked     State = "removed"              // removed from use; the keychain may still hold it
	Legacy      State = "legacy"               // the plaintext metrics.token (C5 §6)
	BadEndpoint State = "endpoint unusable"    // the endpoint URL has no origin, or carries credentials
)

// Credential is an endpoint's credential as resolved now.
type Credential struct {
	State State
	// Why says what the state means for this endpoint, for a reason line.
	Why string
	// Origin is the endpoint's canonical origin ("" for a proxied endpoint).
	Origin string
	// StoredToo: a legacy token is in the file and a record is in the
	// keychain (a Move whose file edit did not happen yet, C5 §6).
	StoredToo bool
	typ       string
	username  string
	secret    string
}

// Usable reports whether requests may carry the credential, or need none.
func (c Credential) Usable() bool {
	switch c.State {
	case NotNeeded, NotSent, Stored, Legacy:
		return true
	}
	return false
}

// Header is the Authorization header value, or "" when none is sent.
func (c Credential) Header() string {
	switch {
	case c.secret == "":
		return ""
	case c.typ == "basic":
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(c.username+":"+c.secret))
	}
	return "Bearer " + c.secret
}

// Redactor removes every wire form of the credential (C5 §5).
func (c Credential) Redactor() safehttp.Redactor {
	if c.typ == "basic" {
		return safehttp.NewRedactor(safehttp.Basic(c.username, c.secret))
	}
	return safehttp.NewRedactor(safehttp.Bearer(c.secret))
}

// Input is what resolving one endpoint's credential reads.
type Input struct {
	Cluster, Kind string
	Spec          config.EndpointConfig
	LegacyToken   string // metrics.token from the file, or ""
	Revoked       bool   // the registry's tombstone (C5 §7)
}

// Resolve reads the endpoint's credential (C5 §3, §6). The legacy token wins
// while it is in the file; a keychain record counts only for the origin it
// was stored for, and only as the type configured.
func Resolve(store secretstore.Store, in Input) Credential {
	out := Credential{}
	if in.Spec.Endpoint == "" {
		out.State, out.Why = NotNeeded, ""
		if in.Spec.Auth != nil || in.LegacyToken != "" {
			out.State, out.Why = NotSent, "credentials are not sent to a proxied or discovered endpoint"
		}
		return out
	}
	if p := config.EndpointURLProblem(in.Spec.Endpoint); p != "" {
		return Credential{State: BadEndpoint, Why: p}
	}
	origin, err := safehttp.OriginOf(in.Spec.Endpoint)
	if err != nil {
		return Credential{State: BadEndpoint, Why: "the endpoint URL has no origin: " + err.Error()}
	}
	out.Origin = origin
	if in.LegacyToken != "" {
		// the file's token is what the person last wrote; it is for whatever
		// origin the file names - the one exception to origin binding
		out.State, out.typ, out.secret = Legacy, "bearer", in.LegacyToken
		if store != nil {
			if _, err := store.Get(Account(in.Cluster, in.Kind)); err == nil {
				out.StoredToo = true
			}
		}
		return out
	}
	if in.Spec.Auth == nil {
		out.State = NotNeeded
		return out
	}
	if in.Revoked {
		out.State, out.Why = Revoked, "the credential was removed from use"
		return out
	}
	if store == nil {
		out.State, out.Why = Unavailable, "keychain unavailable: no credential store"
		return out
	}
	raw, err := store.Get(Account(in.Cluster, in.Kind))
	var kc *secretstore.KeychainError
	switch {
	case errors.Is(err, secretstore.ErrNoKey):
		out.State, out.Why = Missing, "no credential is stored for it - set one in Settings > Observability"
		return out
	case errors.As(err, &kc):
		out.State, out.Why = Unavailable, kc.Error()
		return out
	case err != nil:
		out.State, out.Why = Unavailable, "keychain unavailable: "+err.Error()
		return out
	}
	var rec Record
	switch {
	case json.Unmarshal([]byte(raw), &rec) != nil:
		out.State, out.Why = Unusable, "the stored credential cannot be used (it does not parse) - set it again"
	case rec.V != recordVersion:
		out.State, out.Why = Unusable, fmt.Sprintf("the stored credential cannot be used (record version %d) - set it again", rec.V)
	case rec.Type != in.Spec.Auth.Type:
		out.State, out.Why = Unusable, fmt.Sprintf("the stored credential cannot be used (it is %s, the endpoint is configured for %s) - set it again", clip(rec.Type), in.Spec.Auth.Type)
	case rec.Secret == "":
		out.State, out.Why = Unusable, "the stored credential cannot be used (it is empty) - set it again"
	case rec.Origin != origin:
		out.State, out.Why = OtherOrigin, fmt.Sprintf("the stored credential is for %s; the endpoint is now %s - set it again", clip(rec.Origin), origin)
	default:
		out.State, out.typ, out.username, out.secret = Stored, rec.Type, in.Spec.Auth.Username, rec.Secret
	}
	return out
}

func clip(s string) string {
	if len(s) > 120 {
		return s[:120] + "..."
	}
	return s
}

// Registry holds each endpoint's in-memory credential revision and its
// revocation tombstone (C5 §7). Every cached client records the revision it
// was built under, and a client whose revision moved refuses to send.
type Registry struct {
	mu   sync.Mutex
	rev  map[string]uint64
	tomb map[string]bool
}

// NewRegistry is an empty registry.
func NewRegistry() *Registry { return &Registry{rev: map[string]uint64{}, tomb: map[string]bool{}} }

func key(cluster, kind string) string { return cluster + "\x00" + kind }

// Revision is the endpoint's current credential revision.
func (r *Registry) Revision(cluster, kind string) uint64 {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rev[key(cluster, kind)]
}

// Bump moves the revision: Set, a successful Move, a reload that changed the
// endpoint. A Set also lifts a revocation.
func (r *Registry) Bump(cluster, kind string, lift bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := key(cluster, kind)
	r.rev[k]++
	if lift {
		delete(r.tomb, k)
	}
}

// Revoke tombstones the endpoint's credential and moves the revision, before
// anything else happens to the keychain (C5 §7): no client may load or send
// it from now on, whether or not the keychain delete then succeeds.
func (r *Registry) Revoke(cluster, kind string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := key(cluster, kind)
	r.tomb[k] = true
	r.rev[k]++
}

// Retire moves each of kinds' revisions for a removed cluster (Settings S2
// §2.5), with no revocation: a client built before the removal never
// matches again, whatever is added under the name later, while a kept
// keychain record stays usable under its own checks. Counters only move
// forward; nothing is reset or erased.
func (r *Registry) Retire(cluster string, kinds ...string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, kind := range kinds {
		r.rev[key(cluster, kind)]++
	}
}

// Revoked reports the tombstone.
func (r *Registry) Revoked(cluster, kind string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tomb[key(cluster, kind)]
}

// Live is a gate for a client built under rev: true while the endpoint's
// revision has not moved.
func (r *Registry) Live(cluster, kind string, rev uint64) func() bool {
	if r == nil {
		return func() bool { return true }
	}
	return func() bool { return r.Revision(cluster, kind) == rev }
}
