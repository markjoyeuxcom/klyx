package appbridge

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/moomora/klyx/internal/certmanager"
	"github.com/moomora/klyx/internal/externalsecrets"
	"github.com/moomora/klyx/internal/gitops/argo"
	"github.com/moomora/klyx/internal/gitops/flux"
	"github.com/moomora/klyx/internal/gwapi"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/karpenter"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/workloads"
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
	// Facts the evaluation used (for workloads, fluxPresent), from the same
	// capture as the rows - never read from a later summary (§2a).
	Facts map[string]any `json:"facts,omitempty"`
	// EmittedAtMs is set only while the §6a lab trace is on: the emit time,
	// so the webview probe can time a push from emit to store commit.
	EmittedAtMs int64 `json:"emittedAtMs,omitempty"`
	// Full: Rows is the part's complete row set. Otherwise the message is a
	// delta (slice 3 addendum §3.3) against Base, the Ver it applies to:
	// Upserts are the rows, by UID, whose sent form changed and Removed the
	// UIDs gone. Part, Facts and EvaluatedAt travel on both, so an empty
	// delta still carries a fact-, freshness- or coverage-only change.
	Full    bool     `json:"full"`
	Base    uint64   `json:"base,omitempty"`
	Upserts any      `json:"upserts,omitempty"`
	Removed []string `json:"removed,omitempty"`
}

// rowsConverter turns a part's evaluated rows into its lens DTOs. It copies
// each row's captured evaluation and identity; it never evaluates a rule,
// reads the clock for one, or fetches an object (§2a). Relative fields are
// computed at the capture's EvaluatedAt, never at conversion time.
type rowsConverter func(rows any, at time.Time) (any, error)

var errRowsShape = errors.New("rows of an unexpected shape")

var rowsConverters = map[string]rowsConverter{
	healthsrc.TaskCerts + "/certificates": func(rows any, at time.Time) (any, error) {
		set, err := rowSetOf[certmanager.Certificate](rows)
		if err != nil {
			return nil, err
		}
		out := make([]CertificateDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			dto := certificateFields(r.Data, at)
			dto.Rule = toRuleDTO(r.Eval) // the capture's evaluation, never a new one
			dto.UID, dto.GVR = r.Ref.UID, r.Ref.GVR
			out = append(out, dto)
		}
		return out, nil
	},
	healthsrc.TaskCerts + "/issuers": func(rows any, _ time.Time) (any, error) {
		set, err := rowSetOf[certmanager.Issuer](rows)
		if err != nil {
			return nil, err
		}
		out := make([]IssuerDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			dto := issuerFields(r.Data)
			dto.Rule = toRuleDTO(r.Eval)
			dto.UID, dto.GVR, dto.Generation = r.Ref.UID, r.Ref.GVR, r.Ref.Generation
			out = append(out, dto)
		}
		return out, nil
	},
	healthsrc.TaskFlux + "/reconciliations": func(rows any, at time.Time) (any, error) {
		set, err := rowSetOf[flux.Resource](rows)
		if err != nil {
			return nil, err
		}
		out := make([]FluxResourceDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			dto := fluxResourceFields(r.Data, at)
			dto.Rule = toRuleDTO(r.Eval)
			dto.UID, dto.GVR, dto.Generation = r.Ref.UID, r.Ref.GVR, r.Ref.Generation
			out = append(out, dto)
		}
		return out, nil
	},
	healthsrc.TaskFlux + "/sources": func(rows any, _ time.Time) (any, error) {
		set, err := rowSetOf[flux.Source](rows)
		if err != nil {
			return nil, err
		}
		out := make([]FluxSourceDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			dto := sourceFields(r.Data)
			dto.Rule = toRuleDTO(r.Eval)
			dto.UID, dto.GVR, dto.Generation = r.Ref.UID, r.Ref.GVR, r.Ref.Generation
			out = append(out, dto)
		}
		return out, nil
	},
	healthsrc.TaskArgo + "/applications": func(rows any, _ time.Time) (any, error) {
		set, err := rowSetOf[argo.App](rows)
		if err != nil {
			return nil, err
		}
		out := make([]ArgoAppDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			dto := argoAppFields(r.Data)
			dto.Rule = toRuleDTO(r.Eval)
			dto.UID, dto.GVR, dto.Generation = r.Ref.UID, r.Ref.GVR, r.Ref.Generation
			out = append(out, dto)
		}
		return out, nil
	},
	healthsrc.TaskArgo + "/applicationsets": func(rows any, _ time.Time) (any, error) {
		set, err := rowSetOf[argo.ApplicationSet](rows)
		if err != nil {
			return nil, err
		}
		out := make([]ArgoApplicationSetDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			dto := argoAppSetFields(r.Data)
			dto.Rule = toRuleDTO(r.Eval)
			dto.UID, dto.GVR, dto.Generation = r.Ref.UID, r.Ref.GVR, r.Ref.Generation
			out = append(out, dto)
		}
		return out, nil
	},
	healthsrc.TaskKarpenter + "/nodepools": func(rows any, _ time.Time) (any, error) {
		set, err := rowSetOf[healthsrc.KarpenterPoolRow](rows)
		if err != nil {
			return nil, err
		}
		out := make([]KarpenterPoolDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			dto := karpenterPoolFields(r.Data.Pool)
			dto.Rule = toRuleDTO(r.Eval)
			dto.UID, dto.GVR, dto.Generation = r.Ref.UID, r.Ref.GVR, r.Ref.Generation
			out = append(out, dto)
		}
		return out, nil
	},
	healthsrc.TaskKarpenter + "/nodeclaims": func(rows any, _ time.Time) (any, error) {
		set, err := rowSetOf[karpenter.NodeClaim](rows)
		if err != nil {
			return nil, err
		}
		out := make([]KarpenterClaimDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			dto := karpenterClaimFields(r.Data)
			dto.Rule = toRuleDTO(r.Eval)
			dto.UID, dto.GVR, dto.Generation = r.Ref.UID, r.Ref.GVR, r.Ref.Generation
			out = append(out, dto)
		}
		return out, nil
	},
	healthsrc.TaskGateway + "/gateways": func(rows any, _ time.Time) (any, error) {
		set, err := rowSetOf[gwapi.GatewayRef](rows)
		if err != nil {
			return nil, err
		}
		out := make([]GatewayRefDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			dto := gatewayRefFields(r.Data)
			dto.Rule = toRuleDTO(r.Eval)
			dto.UID, dto.GVR, dto.Generation = r.Ref.UID, r.Ref.GVR, r.Ref.Generation
			out = append(out, dto)
		}
		return out, nil
	},
	healthsrc.TaskGateway + "/listenersets": func(rows any, _ time.Time) (any, error) {
		set, err := rowSetOf[healthsrc.ListenerSetRow](rows)
		if err != nil {
			return nil, err
		}
		out := make([]ListenerSetRowDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			out = append(out, listenerSetRowDTO(r))
		}
		return out, nil
	},
	healthsrc.TaskGateway + "/routes": func(rows any, _ time.Time) (any, error) {
		set, err := rowSetOf[healthsrc.RouteRow](rows)
		if err != nil {
			return nil, err
		}
		out := make([]GatewayRouteRowDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			out = append(out, routeRowDTO(r))
		}
		return out, nil
	},
	healthsrc.TaskNodes + "/nodes": func(rows any, _ time.Time) (any, error) {
		set, err := rowSetOf[workloads.NodeSummary](rows)
		if err != nil {
			return nil, err
		}
		out := make([]NodeSummaryDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			// a verdict part: the captured verdict is in the summary's Health
			dto := toNodeSummaryDTO(r.Data)
			dto.UID, dto.GVR, dto.Generation = r.Ref.UID, r.Ref.GVR, r.Ref.Generation
			dto.AgeSeconds = 0 // time-stable rows: the lens ticks the age from Created (addendum §3.1)
			out = append(out, dto)
		}
		return out, nil
	},
	healthsrc.TaskPods + "/pods": func(rows any, _ time.Time) (any, error) {
		set, err := rowSetOf[workloads.PodSummary](rows)
		if err != nil {
			return nil, err
		}
		out := make([]PodRowDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			// a verdict part: the captured verdict is in the summary's Health;
			// the bounded row (addendum §3.2) leaves the message and the
			// container detail to the UID-fenced detail
			dto := toPodRowDTO(r.Data)
			dto.UID, dto.GVR, dto.Generation = r.Ref.UID, r.Ref.GVR, r.Ref.Generation
			out = append(out, dto)
		}
		return out, nil
	},
	healthsrc.TaskWorkloads + "/workloads": func(rows any, _ time.Time) (any, error) {
		set, err := rowSetOf[workloads.Workload](rows)
		if err != nil {
			return nil, err
		}
		out := make([]WorkloadDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			// a verdict part: the captured verdict is in the workload's Health;
			// fluxPresent travels in the set's facts (§2a)
			dto := toWorkloadDTO(r.Data)
			dto.UID, dto.GVR, dto.Generation = r.Ref.UID, r.Ref.GVR, r.Ref.Generation
			for i := range dto.Pods {
				dto.Pods[i].AgeSeconds = 0 // time-stable rows (addendum §3.1)
			}
			out = append(out, dto)
		}
		return out, nil
	},
	healthsrc.TaskESO + "/externalsecrets": func(rows any, at time.Time) (any, error) {
		set, err := rowSetOf[externalsecrets.ExternalSecret](rows)
		if err != nil {
			return nil, err
		}
		out := make([]ExternalSecretDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			dto := externalSecretFields(r.Data, at)
			dto.Rule = toRuleDTO(r.Eval)
			dto.UID, dto.GVR, dto.Generation = r.Ref.UID, r.Ref.GVR, r.Ref.Generation
			out = append(out, dto)
		}
		return out, nil
	},
	healthsrc.TaskESO + "/stores": func(rows any, _ time.Time) (any, error) {
		set, err := rowSetOf[externalsecrets.Store](rows)
		if err != nil {
			return nil, err
		}
		out := make([]StoreDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			dto := storeFields(r.Data)
			dto.Rule = toRuleDTO(r.Eval)
			dto.UID, dto.GVR, dto.Generation = r.Ref.UID, r.Ref.GVR, r.Ref.Generation
			out = append(out, dto)
		}
		return out, nil
	},
}

// rowSetOf reads a part's rows. nil is a valid publication - a part whose
// counted kinds are all absent is not evaluated - and converts to an empty
// set beside its absent Part, never to an error; any other shape is one.
func rowSetOf[T any](rows any) (healthsrc.RowSet[T], error) {
	if rows == nil {
		return healthsrc.RowSet[T]{}, nil
	}
	set, ok := rows.(healthsrc.RowSet[T])
	if !ok {
		return set, errRowsShape
	}
	return set, nil
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
	out := RowsDTO{Cluster: r.Cluster, Incarnation: r.Incarnation, Task: r.Task, Part: r.Part, EvaluatedAt: r.EvaluatedAt, Rows: rows, Full: true}
	if f, ok := r.Rows.(interface{ RowFacts() map[string]any }); ok {
		out.Facts = f.RowFacts()
	}
	return out, true
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
	key                 lineageKey // its part's delta lineage (addendum §3.3)
	release, unsub      func()
	// ctx lives as long as the subscription: a close cancels any bootstrap
	// still waiting on its capture (§2b).
	ctx    context.Context
	cancel context.CancelFunc
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
		key := lineageKey{cluster: cluster, incarnation: o.Incarnation(), task: task, part: part}
		if s.lineage != nil {
			s.lineage.retain(key) // before subscribing: the first broadcast is kept
		}
		release := o.Acquire(task, rowsOwner(token))
		unsub := o.SubscribeRows(task, part, rowsOwner(token))
		ctx, cancel := context.WithCancel(context.Background())
		sub = &rowsSub{cluster: cluster, task: task, part: part, o: o, key: key, release: release, unsub: unsub, ctx: ctx, cancel: cancel}
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
		sub.cancel() // a bootstrap still waiting answers Closed at once
		sub.unsub()
		sub.release()
		if s.lineage != nil {
			s.lineage.release(sub.key)
		}
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

// bootstrap answers with the part's rows in full. A delta part answers
// with its lineage's published state (addendum §3.3): a fresh capture can
// run ahead of the broadcast, and a subscriber holding a Ver the broadcast
// skips could apply no later delta. Only a part with no published state
// yet captures, and that capture becomes the published state.
func (s *HealthService) bootstrap(token string, sub *rowsSub) RowsReplyDTO {
	if s.lineage != nil {
		if dto, ok := s.lineage.current(sub.key); ok {
			return s.replyOpen(token, dto)
		}
	}
	ctx, cancel := context.WithTimeout(sub.ctx, rowsBootstrapTimeout)
	defer cancel()
	r, ok := sub.o.RowsNowCtx(ctx, sub.task, sub.part)
	if sub.ctx.Err() != nil {
		return RowsReplyDTO{Closed: true} // closed while waiting
	}
	if !ok {
		return RowsReplyDTO{Error: "the cluster's observation ended, or the capture did not answer within " + rowsBootstrapTimeout.String()}
	}
	dto, ok := toRowsDTO(r)
	if !ok {
		return RowsReplyDTO{Error: "rows of " + sub.task + "/" + sub.part + " could not be converted"}
	}
	if s.lineage != nil {
		dto = s.lineage.seedFrom(dto)
	}
	return s.replyOpen(token, dto)
}

func (s *HealthService) replyOpen(token string, dto RowsDTO) RowsReplyDTO {
	g := s.rows
	g.mu.Lock()
	_, closed := g.closed[token]
	g.mu.Unlock()
	if closed { // closed while capturing: never hand back a dead subscription's rows
		return RowsReplyDTO{Closed: true}
	}
	return RowsReplyDTO{Rows: &dto}
}
