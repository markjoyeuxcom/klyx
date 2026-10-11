package appbridge

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/moomora/klyx/internal/certmanager"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/observe"
)

// The shared rows transport (1c slice 3 §2, §2a, §2b): a lens reads its
// task's rows from the cluster's Observer instead of its own watch.

// HealthRowsEventPrefix + "<cluster>:<task>/<part>" carries one part's rows.
const HealthRowsEventPrefix = "health:rows:"

// RowsDTO is one part's rows as the lens's DTOs, with the part (counts,
// coverage, kinds, Ver) from the SAME capture.
type RowsDTO struct {
	Cluster     string       `json:"cluster"`
	Incarnation uint64       `json:"incarnation"`
	Task        string       `json:"task"`
	Part        observe.Part `json:"part"`
	EvaluatedAt time.Time    `json:"evaluatedAt"`
	Rows        any          `json:"rows"`
}

// rowsConverter turns a part's evaluated rows into its lens DTOs. It copies
// each row's captured evaluation and identity; it never evaluates a rule,
// reads the clock for one, or fetches an object (§2a). Relative fields are
// computed at the capture's EvaluatedAt, never at conversion time.
type rowsConverter func(rows any, at time.Time) (any, error)

var errRowsShape = errors.New("rows of an unexpected shape")

var rowsConverters = map[string]rowsConverter{
	healthsrc.TaskCerts + "/certificates": func(rows any, at time.Time) (any, error) {
		set, ok := rows.(healthsrc.RowSet[certmanager.Certificate])
		if !ok {
			return nil, errRowsShape
		}
		out := make([]CertificateDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			dto := toCertificateDTO(r.Data, at)
			dto.Rule = toRuleDTO(r.Eval) // the capture's evaluation, never a new one
			dto.UID = r.Ref.UID
			out = append(out, dto)
		}
		return out, nil
	},
	healthsrc.TaskCerts + "/issuers": func(rows any, at time.Time) (any, error) {
		set, ok := rows.(healthsrc.RowSet[certmanager.Issuer])
		if !ok {
			return nil, errRowsShape
		}
		out := make([]IssuerDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			dto := toIssuerDTO(r.Data)
			dto.Rule = toRuleDTO(r.Eval)
			dto.UID, dto.Generation = r.Ref.UID, r.Ref.Generation
			out = append(out, dto)
		}
		return out, nil
	},
}

// toRowsDTO converts one Rows publication. ok is false for a part without a
// converter (no lens reads it yet) or rows of an unexpected shape.
func toRowsDTO(r observe.Rows) (RowsDTO, bool) {
	conv := rowsConverters[r.Task+"/"+r.Part.ID]
	if conv == nil {
		return RowsDTO{}, false
	}
	rows, err := conv(r.Rows, r.EvaluatedAt)
	if err != nil {
		return RowsDTO{}, false
	}
	return RowsDTO{Cluster: r.Cluster, Incarnation: r.Incarnation, Task: r.Task, Part: r.Part, EvaluatedAt: r.EvaluatedAt, Rows: rows}, true
}

// RowsReplyDTO answers OpenRows and RowsBootstrap. Closed: the token was
// closed (or never opened, for a bootstrap) - the caller drops the reply.
type RowsReplyDTO struct {
	Rows       *RowsDTO `json:"rows,omitempty"`
	RequestGen uint64   `json:"requestGen,omitempty"`
	Closed     bool     `json:"closed,omitempty"`
	Error      string   `json:"error,omitempty"`
}

// rowsBootstrapTimeout bounds one bootstrap capture (§2b).
const rowsBootstrapTimeout = 10 * time.Second

// rowsTombstones bounds the memory of closed tokens. Tokens are unique per
// open (the frontend mints one per subscription), so a token is closed long
// before this many later closes could evict it.
const rowsTombstones = 4096

type rowsSub struct {
	cluster, task, part string
	o                   *observe.Observer
	release, unsub      func()
}

// rowsRegistry tracks lens subscriptions by token. Each token holds its own
// task lease and rows subscription, so closing one part can never release
// another's (§2b); a closed token is tombstoned, so an Open that arrives
// after its Close - or a retry of it - can never resurrect it.
type rowsRegistry struct {
	mu     sync.Mutex
	subs   map[string]*rowsSub
	closed map[string]struct{}
	order  []string
}

func newRowsRegistry() *rowsRegistry {
	return &rowsRegistry{subs: map[string]*rowsSub{}, closed: map[string]struct{}{}}
}

func (g *rowsRegistry) tombstoneLocked(token string) {
	if _, ok := g.closed[token]; ok {
		return
	}
	g.closed[token] = struct{}{}
	g.order = append(g.order, token)
	if len(g.order) > rowsTombstones {
		delete(g.closed, g.order[0])
		g.order = g.order[1:]
	}
}

func (s *HealthService) observerFor(cluster string) *observe.Observer {
	for _, o := range s.observers() {
		if o.Cluster() == cluster {
			return o
		}
	}
	return nil
}

func rowsOwner(token string) observe.Owner { return observe.Owner{Kind: "lens", ID: token} }

// OpenRows subscribes a lens to one part's rows and bootstraps it (§2b):
// it takes the task lease for the token, subscribes, then returns a fresh
// capture. The frontend listens to the part's event BEFORE calling, and
// keeps only strictly newer (incarnation, Ver). A repeated open of a live
// token returns a fresh bootstrap; an open of a closed token is refused.
func (s *HealthService) OpenRows(cluster, task, part, token string) RowsReplyDTO {
	if token == "" {
		return RowsReplyDTO{Error: "a subscription token is required"}
	}
	if rowsConverters[task+"/"+part] == nil {
		return RowsReplyDTO{Error: "no lens rows for " + task + "/" + part}
	}
	g := s.rows
	g.mu.Lock()
	if _, closed := g.closed[token]; closed {
		g.mu.Unlock()
		return RowsReplyDTO{Closed: true}
	}
	sub := g.subs[token]
	if sub != nil && (sub.cluster != cluster || sub.task != task || sub.part != part) {
		g.mu.Unlock()
		return RowsReplyDTO{Error: "the token belongs to another subscription"}
	}
	if sub == nil {
		o := s.observerFor(cluster)
		if o == nil {
			g.mu.Unlock()
			return RowsReplyDTO{Error: "cluster not observed: " + cluster}
		}
		if !o.HasPart(task, part) {
			g.mu.Unlock()
			return RowsReplyDTO{Error: "no part " + task + "/" + part}
		}
		release := o.Acquire(task, rowsOwner(token))
		unsub := o.SubscribeRows(task, part, rowsOwner(token))
		sub = &rowsSub{cluster: cluster, task: task, part: part, o: o, release: release, unsub: unsub}
		g.subs[token] = sub
	}
	g.mu.Unlock()
	reply := s.bootstrap(token, sub)
	if reply.Error != "" {
		s.CloseRows(token) // a failed open releases what it took
	}
	return reply
}

// CloseRows ends a subscription and tombstones its token. Unknown and
// already-closed tokens are a no-op.
func (s *HealthService) CloseRows(token string) {
	g := s.rows
	g.mu.Lock()
	sub := g.subs[token]
	delete(g.subs, token)
	g.tombstoneLocked(token)
	g.mu.Unlock()
	if sub != nil {
		sub.unsub()
		sub.release()
	}
}

// RowsBootstrap is the fenced re-bootstrap (§4 A4): a fresh capture for a
// live token, echoing requestGen. The frontend applies it only if the
// token, incarnation and requestGen are all still current and its Ver is
// newer than the rows held.
func (s *HealthService) RowsBootstrap(token string, requestGen uint64) RowsReplyDTO {
	g := s.rows
	g.mu.Lock()
	sub := g.subs[token]
	g.mu.Unlock()
	if sub == nil {
		return RowsReplyDTO{Closed: true, RequestGen: requestGen}
	}
	reply := s.bootstrap(token, sub)
	reply.RequestGen = requestGen
	return reply
}

func (s *HealthService) bootstrap(token string, sub *rowsSub) RowsReplyDTO {
	ctx, cancel := context.WithTimeout(context.Background(), rowsBootstrapTimeout)
	defer cancel()
	r, ok := sub.o.RowsNowCtx(ctx, sub.task, sub.part)
	if !ok {
		return RowsReplyDTO{Error: "the cluster's observation ended, or the capture did not answer within " + rowsBootstrapTimeout.String()}
	}
	dto, ok := toRowsDTO(r)
	if !ok {
		return RowsReplyDTO{Error: "rows of " + sub.task + "/" + sub.part + " could not be converted"}
	}
	g := s.rows
	g.mu.Lock()
	_, closed := g.closed[token]
	g.mu.Unlock()
	if closed { // closed while capturing: never hand back a dead subscription's rows
		return RowsReplyDTO{Closed: true}
	}
	return RowsReplyDTO{Rows: &dto}
}
