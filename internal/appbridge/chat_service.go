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

	// An investigation's turn reads more (AI A1 §1).
	invTurnDeadline  = 300 * time.Second
	invMaxModelCalls = 12
	invMaxToolCalls  = 24
	invReadWorkers   = 6
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
	git   chatGit     // set once at startup (SetChatGit); nil without Git
	logs  chatHistory // set once at startup (SetChatLogHistory)

	mu      sync.Mutex
	convs   map[string]*conversation // by cluster
	version uint64                   // bumped on every change; orders state replies
}

// NewChatService wires the chat to the registry and the AI snapshot.
func NewChatService(state ChatConnState, aiSvc *AIService, em Emitter) *ChatService {
	return &ChatService{state: state, ai: aiSvc, em: em, now: time.Now, convs: map[string]*conversation{}}
}

type conversation struct {
	id      string
	cluster string
	// key is the conversation's slot: the cluster for chat, invKey(cluster)
	// for an investigation (AI A1 §1); both count in chatMaxConvs.
	key string
	// kind is chat or investigation; anchor is an investigation's object,
	// fenced by UID like an Explain seed.
	kind   string
	anchor *ChatSeedDTO
	// members are an investigation's comparison set, fixed before its first
	// question (A-C2); membersRead, once a member's data was read: every
	// later send carries it and is fenced by every member
	members     []member
	membersRead bool
	conn        ChatConn
	rev         uint64
	provider    ai.ProviderID
	model       string
	origin      string
	mode        string // tools | snapshot
	ended       string
	trimmed     bool
	removed     int

	exchanges []*exchange
	active    *activeTurn
	// reads numbers the conversation's ledger for citation (AI A1 §2):
	// never reset, so an ID removed by retention is never reused
	reads int
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
	seed      *ChatSeedDTO
	seedData  string // the seeded object's envelope, sent as data with the question
	// findings are an investigation turn's, as Klyx labels them (AI A1 §4);
	// reported holds the model's report until the turn ends, when it is
	// labelled again against the turn's settled ledger
	findings     *FindingsDTO
	reported     *findingsInput
	findingCalls int
	// a snapshot investigation's sections left out to fit (read IDs) and
	// the plan steps its budget stopped
	snapshotDropped []string
	snapshotSkipped []string
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
	Findings *FindingsDTO       `json:"findings,omitempty"`
}

// ChatStateDTO is the drawer's whole view of a cluster's conversation.
type ChatStateDTO struct {
	ConversationID string            `json:"conversationId,omitempty"`
	Cluster        string            `json:"cluster"`
	Kind           string            `json:"kind"`             // chat | investigation
	Anchor         *ChatSeedDTO      `json:"anchor,omitempty"` // an investigation's object
	Comparison     []string          `json:"comparison,omitempty"`
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
	// Notice is a recoverable problem with the last request (a question too
	// long, one already running): the conversation stays usable.
	Notice string `json:"notice,omitempty"`
	// Version orders replies: the drawer applies a state only when it is
	// not older than the one it shows (Codex review of 143dc6d).
	Version uint64 `json:"version"`
}

// ChatSeedDTO names the object an "Ask a follow-up" is about, as Explain
// targeted it: an Observer row (task, part, uid), a generic object (GVR,
// namespace, name, uid) or a Helm release. The backend resolves and reads
// it, and refuses a same-name replacement by UID (spec §§7, 10).
type ChatSeedDTO struct {
	Kind      string `json:"kind"` // row | object | helm
	Task      string `json:"task,omitempty"`
	Part      string `json:"part,omitempty"`
	UID       string `json:"uid,omitempty"`
	Group     string `json:"group,omitempty"`
	Version   string `json:"version,omitempty"`
	Plural    string `json:"plural,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name,omitempty"`
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

// invIDPrefix marks an investigation's conversation ID, so a request for
// one that is gone is answered from its own slot, never with the cluster's
// chat (A1 implementation review, finding 11).
const invIDPrefix = "inv-"

// slotOf is the slot a conversation ID was made for.
func slotOf(cluster, id string) string {
	if strings.HasPrefix(id, invIDPrefix) {
		return invKey(cluster)
	}
	return cluster
}

const (
	kindChat          = "chat"
	kindInvestigation = "investigation"
)

// invKey is an investigation's slot: one per cluster, beside its chat. A
// NUL never appears in a cluster's name.
func invKey(cluster string) string { return "\x00investigation:" + cluster }

// turnLimits are a conversation's per-turn budgets.
type turnLimits struct {
	models, tools, workers int
	deadline               time.Duration
}

func (c *conversation) limits() turnLimits {
	if c.kind == kindInvestigation {
		return turnLimits{invMaxModelCalls, invMaxToolCalls, invReadWorkers, invTurnDeadline}
	}
	return turnLimits{chatMaxModelCalls, chatMaxToolCalls, chatReadWorkers, chatTurnDeadline}
}

// convLocked finds a cluster's conversation by its ID, in either slot.
func (s *ChatService) convLocked(cluster, id string) *conversation {
	for _, key := range []string{cluster, invKey(cluster)} {
		if c := s.convs[key]; c != nil && c.id == id {
			return c
		}
	}
	return nil
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
	return s.startLocked(cluster, cluster, kindChat, nil)
}

// StartInvestigation returns the cluster's investigation of anchor, starting
// one when there is none or the last one ended (AI A1 §1). Another open
// investigation on the cluster is refused: one at a time per cluster.
func (s *ChatService) StartInvestigation(cluster string, anchor ChatSeedDTO) ChatStateDTO {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := invKey(cluster)
	refuse := func(msg string) ChatStateDTO {
		return ChatStateDTO{Cluster: cluster, Kind: kindInvestigation, Error: msg, Protected: s.ai.protected(cluster), Exchanges: []ChatExchangeDTO{}, Version: s.version}
	}
	if anchor.Kind != "row" && anchor.Kind != "object" {
		return refuse("an investigation starts from an object Klyx shows")
	}
	if err := checkSeed(&anchor); err != nil {
		return refuse("the object to investigate is not valid: " + err.Error())
	}
	if anchor.UID == "" {
		return refuse("the object to investigate has no UID: Klyx pins an investigation by it")
	}
	if c := s.convs[key]; c != nil && c.ended == "" {
		if *c.anchor == anchor {
			return s.stateLocked(c)
		}
		return refuse(fmt.Sprintf("an investigation of %s is open on this cluster; clear it first", c.anchor.Name))
	}
	a := anchor
	return s.startLocked(cluster, key, kindInvestigation, &a)
}

// startLocked starts a conversation in a slot, or refuses.
func (s *ChatService) startLocked(cluster, key, kind string, anchor *ChatSeedDTO) ChatStateDTO {
	// A refusal changes nothing; it carries the current version (never an
	// older one) and its Error marks it a refusal, which the drawer shows
	// as the request's outcome and never applies as the conversation
	// (Codex reviews of 0725e47 and c4ae925).
	refuse := func(msg string) ChatStateDTO {
		return ChatStateDTO{Cluster: cluster, Kind: kind, Error: msg, Protected: s.ai.protected(cluster), Exchanges: []ChatExchangeDTO{}, Version: s.version}
	}
	st := s.ai.load()
	if refusal := s.refusal(st, cluster); refusal != "" {
		return refuse(refusal)
	}
	conn, usable := s.state(cluster)
	if conn == nil || !usable {
		return refuse("the cluster is not connected")
	}
	if len(s.convs) >= chatMaxConvs && s.convs[key] == nil {
		return refuse(fmt.Sprintf("at most %d conversations at once; clear one first", chatMaxConvs))
	}
	mode := "snapshot"
	if _, ok := st.Provider.(ai.Converser); ok {
		mode = "tools"
	}
	id := newID()
	if kind == kindInvestigation {
		id = invIDPrefix + id
	}
	c := &conversation{id: id, cluster: cluster, key: key, kind: kind, anchor: anchor, conn: conn, rev: st.Rev, provider: st.ProviderID, model: st.Model, origin: st.Origin, mode: mode}
	s.convs[key] = c
	s.version++
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
	return s.currentLocked(cluster, cluster)
}

// InvestigationState returns the cluster's investigation as it stands.
func (s *ChatService) InvestigationState(cluster string) ChatStateDTO {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currentLocked(cluster, invKey(cluster))
}

// currentLocked is a slot's conversation as it stands, or the empty state
// when it has none.
func (s *ChatService) currentLocked(cluster, key string) ChatStateDTO {
	if c := s.convs[key]; c != nil {
		return s.stateLocked(c)
	}
	kind := kindChat
	if key != cluster {
		kind = kindInvestigation
	}
	return ChatStateDTO{Cluster: cluster, Kind: kind, Protected: s.ai.protected(cluster), Exchanges: []ChatExchangeDTO{}, Version: s.version}
}

// obsoleteLocked answers a request for a conversation that is no longer
// the cluster's: the reply is the conversation that is current, never an
// empty state stamped with the current version, and the refusal is its
// notice (Codex review of c4ae925).
func (s *ChatService) obsoleteLocked(cluster, key, msg string) ChatStateDTO {
	st := s.currentLocked(cluster, key)
	if st.ConversationID != "" {
		msg += "; this is the current conversation"
	} else {
		msg += "; start a new one"
	}
	st.Notice = msg
	return st
}

func (s *ChatService) stateLocked(c *conversation) ChatStateDTO {
	out := ChatStateDTO{ConversationID: c.id, Cluster: c.cluster, Kind: c.kind, Anchor: c.anchor, Mode: c.mode, Provider: string(c.provider), Model: c.model, Origin: c.origin,
		Protected: s.ai.protected(c.cluster), Ended: c.ended, Trimmed: c.trimmed, Removed: c.removed, Active: c.active != nil, Exchanges: []ChatExchangeDTO{},
		Version: s.version}
	for _, e := range c.exchanges {
		out.Exchanges = append(out.Exchanges, e.dto())
	}
	for _, m := range c.members {
		out.Comparison = append(out.Comparison, m.cluster)
	}
	return out
}

// dto is a detached copy: a running turn keeps appending reads and
// updating call records, so nothing returned may alias them (Codex review
// of 7b57456). Called under the service lock.
func (e *exchange) dto() ChatExchangeDTO {
	reads := make([]ChatRead, 0, len(e.reads))
	for _, r := range e.reads {
		r.Refs = append([]ChatRef(nil), r.Refs...)
		reads = append(reads, r)
	}
	calls := make([]ChatModelCallDTO, 0, len(e.calls))
	for _, c := range e.calls {
		c.Tools = append([]string(nil), c.Tools...)
		calls = append(calls, c)
	}
	out := ChatExchangeDTO{ID: e.id, Question: e.question, Answer: e.answer, Outcome: e.outcome, Error: e.err, Reads: reads, Calls: calls,
		Snapshot: append([]string(nil), e.snapshot...), Note: e.note, At: e.at.UTC().Format(time.RFC3339)}
	if e.findings != nil {
		f := *e.findings // the turn never changes a recorded one; copied for the reply
		out.Findings = &f
	}
	return out
}

// Stop cancels the conversation's active turn, if it is that conversation's.
func (s *ChatService) Stop(cluster, conversationID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.convLocked(cluster, conversationID); c != nil && c.active != nil {
		c.active.stopped = true
		c.active.cancel()
		s.version++
	}
}

// Clear ends the conversation and forgets it; any active turn is cancelled
// and its late result dropped.
func (s *ChatService) Clear(cluster, conversationID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.convLocked(cluster, conversationID); c != nil {
		if c.active != nil {
			c.active.stopped = true
			c.active.cancel()
		}
		delete(s.convs, c.key)
		s.version++
	}
}

// Ask runs one turn for the operator's question and returns the
// conversation afterwards. It blocks until the turn ends (at most the turn
// deadline); Stop and Clear end it sooner.
func (s *ChatService) Ask(cluster, conversationID, question string, seed *ChatSeedDTO) ChatStateDTO {
	question = strings.TrimSpace(question)
	s.mu.Lock()
	c := s.convLocked(cluster, conversationID)
	notice := func(msg string) ChatStateDTO {
		st := s.stateLocked(c)
		st.Notice = msg
		s.mu.Unlock()
		return st
	}
	// the first turn of an investigation is the investigation (AI A1 §1): it
	// reads the anchor, and needs no question of the operator's
	first := c != nil && c.kind == kindInvestigation && len(c.exchanges) == 0
	if first && question == "" {
		question = "Investigate why this object is failing, and end with your findings."
	}
	switch {
	case c == nil:
		st := s.obsoleteLocked(cluster, slotOf(cluster, conversationID), "that conversation has ended")
		s.mu.Unlock()
		return st
	case first && seed != nil:
		return notice("an investigation reads its own object; ask without a follow-up target")
	case c.ended != "":
		return notice("this conversation has ended; start a new one")
	case c.active != nil:
		return notice("a question is already being answered; stop it or wait")
	case question == "":
		return notice("ask a question")
	case len(question) > chatQuestionMax:
		return notice(fmt.Sprintf("the question is longer than %d KiB (UTF-8); shorten it", chatQuestionMax>>10))
	}
	if seed != nil {
		if err := checkSeed(seed); err != nil {
			return notice("the follow-up target is not valid: " + err.Error())
		}
	}
	if first {
		seed = c.anchor // validated when the investigation started
	}
	lim := c.limits()
	ctx, cancel := context.WithTimeout(context.Background(), lim.deadline)
	turn := &activeTurn{id: newID(), cancel: cancel}
	c.active = turn
	ex := &exchange{id: newID(), question: question, at: s.now(), seed: seed}
	c.exchanges = append(c.exchanges, ex)
	s.version++
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
	if s.convs[c.key] != c || c.active != turn {
		// Cleared or replaced while the turn ran: the late result is dropped.
		return s.obsoleteLocked(cluster, c.key, "that conversation was cleared before its answer arrived")
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
		ex.outcome, ex.err = "failed", fmt.Sprintf("no answer within %s", lim.deadline)
	default:
		ex.outcome, ex.err = "failed", chatScrub(modelOrChatError(err), nil)
	}
	var ended *endError
	if errors.As(err, &ended) {
		c.ended = ended.reason
	}
	if c.kind == kindInvestigation && ex.reported != nil {
		// reads in the report's batch, and any after it, settle first:
		// Klyx's labels and missing evidence never depend on when the
		// model chose to report (A1 implementation review, finding 8)
		ledger := ledgerLocked(c)
		f := labelFindings(*ex.reported, ledger, anchorOf(c.anchor, ledger))
		ex.findings, ex.reported = &f, nil
	}
	if c.kind == kindInvestigation && ex.findings == nil && ex.outcome == "answered" {
		// the model reported nothing: Klyx's own missing evidence still shows
		ledger := ledgerLocked(c)
		f := labelFindings(findingsInput{}, ledger, anchorOf(c.anchor, ledger))
		f.Note = "the model reported no findings this turn"
		ex.findings = &f
	}
	s.retainLocked(c)
	s.version++
	return s.stateLocked(c)
}

// checkSeed validates a follow-up target before anything is read.
func checkSeed(sd *ChatSeedDTO) error {
	switch sd.Kind {
	case "row":
		if !idRe.MatchString(sd.Task) || !idRe.MatchString(sd.Part) || sd.UID == "" {
			return errors.New("a row target needs its area, part and UID")
		}
	case "object":
		if sd.UID == "" || sd.Version == "" || !kindRe.MatchString(sd.Plural) {
			return errors.New("an object target needs its resource and UID")
		}
		if err := firstErr(checkGroup(sd.Group), checkName("namespace", sd.Namespace, false), checkName("name", sd.Name, true)); err != nil {
			return err
		}
	case "helm":
		if err := firstErr(checkName("namespace", sd.Namespace, true), checkName("name", sd.Name, true)); err != nil {
			return err
		}
	default:
		return errors.New("unknown target kind")
	}
	if len(sd.UID) > 64 {
		return errors.New("the UID is not valid")
	}
	return nil
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
		e.msgs, e.seedData, e.seed, e.noHistory = nil, "", nil, true
		e.note = "this exchange was too large to keep for follow-ups"
		if exchangeBytes(e) > chatRetainBytes {
			c.exchanges = nil
			c.removed++
		}
	}
}

func exchangeBytes(e *exchange) int {
	n := len(e.question) + len(e.answer) + len(e.err) + len(e.note) + len(e.seedData)
	if e.seed != nil {
		b, _ := json.Marshal(e.seed)
		n += len(b)
	}
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
