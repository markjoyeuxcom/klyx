package fleet

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/moomora/klyx/internal/readfence"
)

// Receipt is one write's own witness (A3 §5.1): the written object's
// identity, generation and resourceVersion from the server's response, and
// the token this request wrote. A field the response does not carry stays
// zero (a Scale has no generation; a typed delete returns no object).
type Receipt struct {
	UID             string
	Generation      int64
	ResourceVersion string
	// Token is what this request wrote to be recognised by: a restart's
	// restartedAt, a reconcile's requestedAt.
	Token string
	// PriorGeneration is the armed read's generation, where the verb read
	// one: a restart whose receipt is not above it wrote no new template.
	PriorGeneration int64
}

// Receipts collects the receipts of the writes made under one context. A
// verb notes its receipt only when the caller attached a recorder, so the
// manual verbs' signatures stay as they are (A3 §4.4).
type Receipts struct {
	mu       sync.Mutex
	list     []Receipt
	attempts []Attempt
}

// Attempt is a write about to be sent, with the token it carries: a
// restart's restartedAt, a reconcile's requestedAt. It is noted before the
// request goes, so a write whose answer is lost still has its own token
// (AI A3 stage 1 review): an attempt is never a receipt.
type Attempt struct {
	UID   string
	Token string
}

type receiptsKey struct{}

// WithReceipts attaches a recorder to ctx, and a write tracker so a fence
// that refuses a write before sending it says so (readfence.ErrNotForwarded).
func WithReceipts(ctx context.Context) (context.Context, *Receipts) {
	r := &Receipts{}
	return context.WithValue(readfence.WithWriteTracker(ctx), receiptsKey{}, r), r
}

// AttemptOf returns the last attempted write to uid with a token.
func (r *Receipts) AttemptOf(uid string) (Attempt, bool) {
	if r == nil {
		return Attempt{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.attempts) - 1; i >= 0; i-- {
		if r.attempts[i].UID == uid {
			return r.attempts[i], true
		}
	}
	return Attempt{}, false
}

// NoteAttempt records a write's token before it is sent; a ClusterConn
// stand-in calls it the same way.
func NoteAttempt(ctx context.Context, a Attempt) {
	r, _ := ctx.Value(receiptsKey{}).(*Receipts)
	if r == nil || a.UID == "" {
		return
	}
	r.mu.Lock()
	r.attempts = append(r.attempts, a)
	r.mu.Unlock()
}

var (
	tokenMu   sync.Mutex
	lastToken time.Time
)

// attemptToken is one write's own token (AI A3 §1): the clock's reading,
// made strictly later than every token this process issued before, so an
// unchanged or stepped-back clock, or two writes at once, never repeat one.
// Its format stays the clock's RFC3339Nano, as Flux and kubectl write it.
func attemptToken(now time.Time) string {
	tokenMu.Lock()
	defer tokenMu.Unlock()
	t := now.UTC()
	if !t.After(lastToken) {
		t = lastToken.Add(time.Nanosecond)
	}
	lastToken = t
	return t.Format(time.RFC3339Nano)
}

// Of returns the receipt of the write to uid, the last one when a verb
// wrote it more than once.
func (r *Receipts) Of(uid string) (Receipt, bool) {
	if r == nil {
		return Receipt{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.list) - 1; i >= 0; i-- {
		if r.list[i].UID == uid {
			return r.list[i], true
		}
	}
	return Receipt{}, false
}

// NoteReceipt records rc under ctx's recorder, if there is one. The fleet's
// verbs call it after a write the server accepted; a ClusterConn stand-in
// (a test's fake) calls it the same way.
func NoteReceipt(ctx context.Context, rc Receipt) {
	r, _ := ctx.Value(receiptsKey{}).(*Receipts)
	if r == nil || rc.UID == "" {
		return
	}
	r.mu.Lock()
	r.list = append(r.list, rc)
	r.mu.Unlock()
}

type tokenKey struct{}

// withToken carries the token a write is about to make, so the write's
// receipt holds it (a reconcile's requestedAt).
func withToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, tokenKey{}, token)
}

func tokenOf(ctx context.Context) string {
	s, _ := ctx.Value(tokenKey{}).(string)
	return s
}

// ErrNotSent marks a refusal Klyx made before any write was sent: the
// arming, a version gate, "not offered", no change (A3 §5.1). Nothing of
// such a request can have landed.
var ErrNotSent = errors.New("nothing was sent")

// notSentError keeps the refusal's own text and adds ErrNotSent to its
// chain.
type notSentError struct{ err error }

func (e notSentError) Error() string   { return e.err.Error() }
func (e notSentError) Unwrap() []error { return []error{e.err, ErrNotSent} }

// notSent marks err as a refusal before any write; nil stays nil.
func notSent(err error) error {
	if err == nil || errors.Is(err, ErrNotSent) {
		return err
	}
	return notSentError{err}
}

// Write outcomes (A3 §5.1).
const (
	OutcomeWritten    = "written"
	OutcomeRefused    = "refused"
	OutcomeUnverified = "unverified"
)

// WriteOutcome classifies a write's result: written (no error); refused,
// either before anything was sent (notSent) or by the server (a definite
// refusal, or the UID guard's ErrReplaced); otherwise unverified - a write
// was sent and its answer lost, so it may have landed.
func WriteOutcome(err error) (outcome string, wasNotSent bool) {
	switch {
	case err == nil:
		return OutcomeWritten, false
	case errors.Is(err, ErrNotSent), errors.Is(err, readfence.ErrNotForwarded):
		return OutcomeRefused, true
	case stepRefused(err):
		return OutcomeRefused, false
	}
	return OutcomeUnverified, false
}
