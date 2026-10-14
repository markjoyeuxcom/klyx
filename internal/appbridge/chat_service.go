package appbridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/moomora/klyx/internal/ai"
)

// ChatService is the cluster chat (spec 2026-10-14 §5): one conversation per
// cluster, owned here. Only the operator's question text comes from the
// frontend; system prompts, tool calls and results, and provider
// continuation stay in the backend. Read-only: no tool changes anything.

const (
	chatTurnDeadline   = 120 * time.Second
	chatMaxModelCalls  = 6
	chatMaxToolCalls   = 8
	chatReadWorkers    = 4
	chatQuestionMax    = 4 * 1024
	chatAnswerMax      = 32 * 1024
	chatRequestBudget  = 384 * 1024
	chatRetainExchange = 20
	chatRetainBytes    = 1024 * 1024
	chatMaxConvs       = 8
	chatSnapshotMax    = 48 * 1024
	chatSnapshotPrior  = 16 * 1024
)

// ChatConnState reports a cluster's current connection: the conn the
// registry holds now, and whether it is usable. A conversation compares it
// with the conn it pinned.
type ChatConnState func(cluster string) (conn ChatConn, usable bool)

// ChatService holds conversations and runs turns.
type ChatService struct {
	state ChatConnState
	ai    *AIService
	em    Emitter
	now   func() time.Time

	mu    sync.Mutex
	convs map[string]*conversation // by cluster
}

// NewChatService wires the chat to the registry and the AI snapshot.
func NewChatService(state ChatConnState, aiSvc *AIService, em Emitter) *ChatService {
	return &ChatService{state: state, ai: aiSvc, em: em, now: time.Now, convs: map[string]*conversation{}}
}

type conversation struct {
	id       string
	cluster  string
	conn     ChatConn
	rev      uint64
	provider ai.ProviderID
	model    string
	origin   string
	mode     string // tools | snapshot
	ended    string
	trimmed  bool
	removed  int

	exchanges []*exchange
	active    *activeTurn
}

// activeTurn is the one running turn. stopped is set under the service lock
// by Stop or Clear; from then on nothing the turn produces is published
// (Codex review of 5d8003e).
type activeTurn struct {
	id      string
	cancel  context.CancelFunc
	stopped bool
}

// exchange is one question and everything it caused.
type exchange struct {
	id       string
	question string
	msgs     []ai.ConvMessage // assistant and tool steps (tools mode)
	answer   string
	outcome  string // answered | failed | cancelled
	err      string
	reads    []ChatRead
	calls    []ChatModelCallDTO
	snapshot []string // what a snapshot included (snapshot mode)
	at       time.Time
	// noHistory: the exchange is kept for the drawer but too large to send
	// again as history (its steps were dropped by the retention bound).
	noHistory bool
	note      string
}

// ChatModelCallDTO is one attempted model call, for the Activity log: never
// text. Outcome is sent (in flight), completed, failed, cancelled or
// tools-unsupported.
type ChatModelCallDTO struct {
	Provider string   `json:"provider"`
	Model    string   `json:"model"`
	Mode     string   `json:"mode"`
	Tools    []string `json:"tools,omitempty"`
	Outcome  string   `json:"outcome"`
}

// ChatExchangeDTO is one question and its outcome.
type ChatExchangeDTO struct {
	ID       string             `json:"id"`
	Question string             `json:"question"`
	Answer   string             `json:"answer,omitempty"`
	Outcome  string             `json:"outcome"`
	Error    string             `json:"error,omitempty"`
	Reads    []ChatRead         `json:"reads"`
	Calls    []ChatModelCallDTO `json:"calls"`
	Snapshot []string           `json:"snapshot,omitempty"`
	Note     string             `json:"note,omitempty"`
	At       string             `json:"at"`
}

// ChatStateDTO is the drawer's whole view of a cluster's conversation.
type ChatStateDTO struct {
	ConversationID string            `json:"conversationId,omitempty"`
	Cluster        string            `json:"cluster"`
	Mode           string            `json:"mode,omitempty"`
	Provider       string            `json:"provider,omitempty"`
	Model          string            `json:"model,omitempty"`
	Origin         string            `json:"origin,omitempty"`
	Protected      bool              `json:"protected"`
	Ended          string            `json:"ended,omitempty"`
	Error          string            `json:"error,omitempty"`
	Trimmed        bool              `json:"trimmed"`
	Removed        int               `json:"removed"`
	Active         bool              `json:"active"`
	Exchanges      []ChatExchangeDTO `json:"exchanges"`
}

// ChatReadEventDTO is pushed on "chat:<conversationId>" as each read
// finishes, so the drawer shows the ledger while a turn runs.
type ChatReadEventDTO struct {
	ExchangeID string   `json:"exchangeId"`
	Read       ChatRead `json:"read"`
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Start returns the cluster's conversation, starting one when there is none
// or the last one ended. It refuses when AI is unavailable, egress to this
// cluster is not allowed, or the cluster is not connected.
func (s *ChatService) Start(cluster string) ChatStateDTO {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.convs[cluster]; c != nil && c.ended == "" {
		return s.stateLocked(c)
	}
	st := s.ai.load()
	if refusal := s.refusal(st, cluster); refusal != "" {
		return ChatStateDTO{Cluster: cluster, Error: refusal, Protected: s.ai.protected(cluster)}
	}
	conn, usable := s.state(cluster)
	if conn == nil || !usable {
		return ChatStateDTO{Cluster: cluster, Error: "the cluster is not connected", Protected: s.ai.protected(cluster)}
	}
	if len(s.convs) >= chatMaxConvs && s.convs[cluster] == nil {
		return ChatStateDTO{Cluster: cluster, Error: fmt.Sprintf("at most %d conversations at once; clear one first", chatMaxConvs)}
	}
	mode := "snapshot"
	if _, ok := st.Provider.(ai.Converser); ok {
		mode = "tools"
	}
	c := &conversation{id: newID(), cluster: cluster, conn: conn, rev: st.Rev, provider: st.ProviderID, model: st.Model, origin: st.Origin, mode: mode}
	s.convs[cluster] = c
	return s.stateLocked(c)
}

// refusal is why a chat cannot run on this snapshot for this cluster.
func (s *ChatService) refusal(st *AIState, cluster string) string {
	if st.Provider == nil {
		if st.Problem != "" {
			return "AI is unavailable: " + st.Problem
		}
		return "AI is off. Choose a provider in Settings > AI."
	}
	if problem := notReady(st); problem != "" {
		return "AI is unavailable: " + problem
	}
	if !s.ai.egressAllowed(st, cluster) {
		return egressRefusal(cluster)
	}
	return ""
}

// State returns the cluster's conversation as it stands.
func (s *ChatService) State(cluster string) ChatStateDTO {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.convs[cluster]
	if c == nil {
		return ChatStateDTO{Cluster: cluster, Protected: s.ai.protected(cluster), Exchanges: []ChatExchangeDTO{}}
	}
	return s.stateLocked(c)
}

func (s *ChatService) stateLocked(c *conversation) ChatStateDTO {
	out := ChatStateDTO{ConversationID: c.id, Cluster: c.cluster, Mode: c.mode, Provider: string(c.provider), Model: c.model, Origin: c.origin,
		Protected: s.ai.protected(c.cluster), Ended: c.ended, Trimmed: c.trimmed, Removed: c.removed, Active: c.active != nil, Exchanges: []ChatExchangeDTO{}}
	for _, e := range c.exchanges {
		out.Exchanges = append(out.Exchanges, e.dto())
	}
	return out
}

func (e *exchange) dto() ChatExchangeDTO {
	reads := e.reads
	if reads == nil {
		reads = []ChatRead{}
	}
	calls := e.calls
	if calls == nil {
		calls = []ChatModelCallDTO{}
	}
	return ChatExchangeDTO{ID: e.id, Question: e.question, Answer: e.answer, Outcome: e.outcome, Error: e.err, Reads: reads, Calls: calls,
		Snapshot: e.snapshot, Note: e.note, At: e.at.UTC().Format(time.RFC3339)}
}

// Stop cancels the conversation's active turn, if it is that conversation's.
func (s *ChatService) Stop(cluster, conversationID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.convs[cluster]; c != nil && c.id == conversationID && c.active != nil {
		c.active.stopped = true
		c.active.cancel()
	}
}

// Clear ends the conversation and forgets it; any active turn is cancelled
// and its late result dropped.
func (s *ChatService) Clear(cluster, conversationID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.convs[cluster]; c != nil && c.id == conversationID {
		if c.active != nil {
			c.active.stopped = true
			c.active.cancel()
		}
		delete(s.convs, cluster)
	}
}

// Ask runs one turn for the operator's question and returns the
// conversation afterwards. It blocks until the turn ends (at most the turn
// deadline); Stop and Clear end it sooner.
func (s *ChatService) Ask(cluster, conversationID, question string) ChatStateDTO {
	question = strings.TrimSpace(question)
	s.mu.Lock()
	c := s.convs[cluster]
	switch {
	case c == nil || c.id != conversationID:
		s.mu.Unlock()
		return ChatStateDTO{Cluster: cluster, Error: "this conversation has ended; start a new one"}
	case c.ended != "":
		st := s.stateLocked(c)
		s.mu.Unlock()
		return st
	case c.active != nil:
		st := s.stateLocked(c)
		st.Error = "a question is already being answered; stop it or wait"
		s.mu.Unlock()
		return st
	case question == "":
		st := s.stateLocked(c)
		st.Error = "ask a question"
		s.mu.Unlock()
		return st
	case len(question) > chatQuestionMax:
		st := s.stateLocked(c)
		st.Error = fmt.Sprintf("the question is longer than %d KiB", chatQuestionMax>>10)
		s.mu.Unlock()
		return st
	}
	ctx, cancel := context.WithTimeout(context.Background(), chatTurnDeadline)
	turn := &activeTurn{id: newID(), cancel: cancel}
	c.active = turn
	ex := &exchange{id: newID(), question: question, at: s.now()}
	c.exchanges = append(c.exchanges, ex)
	s.mu.Unlock()

	defer cancel()
	r := &turnRunner{s: s, c: c, turn: turn, ex: ex}
	answer, err := r.run(ctx)
	if err == nil {
		// An answer is published only if the turn was not stopped and the
		// pinned conn and settings still hold when it arrives.
		if ctx.Err() != nil {
			err = ctx.Err()
		} else if _, gerr := r.guard(); gerr != nil {
			err = gerr
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.convs[cluster] != c || c.active != turn {
		// Cleared or replaced while the turn ran: the late result is dropped.
		return ChatStateDTO{Cluster: cluster, Error: "this conversation was cleared"}
	}
	c.active = nil
	if turn.stopped && err == nil {
		err = context.Canceled // stopped: a late success is never an answer
	}
	switch {
	case err == nil:
		ex.answer, ex.outcome = capAnswer(answer), "answered"
	case errors.Is(err, context.Canceled):
		ex.outcome, ex.err = "cancelled", "stopped"
	case errors.Is(err, context.DeadlineExceeded):
		ex.outcome, ex.err = "failed", fmt.Sprintf("no answer within %s", chatTurnDeadline)
	default:
		ex.outcome, ex.err = "failed", chatScrub(modelOrChatError(err), nil)
	}
	var ended *endError
	if errors.As(err, &ended) {
		c.ended = ended.reason
	}
	s.retainLocked(c)
	return s.stateLocked(c)
}

func capAnswer(a string) string {
	if len(a) <= chatAnswerMax {
		return a
	}
	return a[:chatAnswerMax] + "\n\n…[answer truncated]"
}

// modelOrChatError maps a turn failure to text: a model outcome through the
// shared Explain mapping, a chat condition as it is.
func modelOrChatError(err error) string {
	var ce *chatError
	if errors.As(err, &ce) {
		return ce.msg
	}
	var ee *endError
	if errors.As(err, &ee) {
		return ee.reason
	}
	return modelError(err)
}

// chatError is a turn failure that is Klyx's own decision.
type chatError struct{ msg string }

func (e *chatError) Error() string { return e.msg }

// endError ends the conversation itself (disconnect, settings change).
type endError struct{ reason string }

func (e *endError) Error() string { return e.reason }

// retainLocked bounds what a conversation keeps (spec §5.2): at most
// chatRetainExchange exchanges and chatRetainBytes, every retained field
// counted, failed and cancelled exchanges too; the oldest go first. A
// single exchange over the bound keeps its question, answer and ledger but
// drops its steps, so it is no longer sent as history (Codex review of
// 5d8003e).
func (s *ChatService) retainLocked(c *conversation) {
	size := func() int {
		n := 0
		for _, e := range c.exchanges {
			n += exchangeBytes(e)
		}
		return n
	}
	for len(c.exchanges) > 1 && (len(c.exchanges) > chatRetainExchange || size() > chatRetainBytes) {
		c.exchanges = c.exchanges[1:]
		c.removed++
	}
	if len(c.exchanges) == 1 && size() > chatRetainBytes {
		e := c.exchanges[0]
		e.msgs, e.noHistory = nil, true
		e.note = "this exchange was too large to keep for follow-ups"
		if exchangeBytes(e) > chatRetainBytes {
			c.exchanges = nil
			c.removed++
		}
	}
}

func exchangeBytes(e *exchange) int {
	n := len(e.question) + len(e.answer) + len(e.err) + len(e.note)
	for _, m := range e.msgs {
		n += msgBytes(m)
	}
	for _, s := range e.snapshot {
		n += len(s)
	}
	reads, _ := json.Marshal(e.reads)
	calls, _ := json.Marshal(e.calls)
	return n + len(reads) + len(calls)
}

func msgBytes(m ai.ConvMessage) int {
	b, _ := json.Marshal(m)
	return len(b)
}

// emit pushes one ledger entry while the turn runs.
func (s *ChatService) emit(c *conversation, ex *exchange, r ChatRead) {
	if s.em != nil {
		s.em.Emit("chat:"+c.id, ChatReadEventDTO{ExchangeID: ex.id, Read: r})
	}
}
