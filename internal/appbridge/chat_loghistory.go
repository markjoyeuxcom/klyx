package appbridge

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/moomora/klyx/internal/ai"
)

// AI A1 §3.6: get_log_history, the AI-safe Loki adapter. It reads one page
// through the log history service (its modes, selectors and source fence)
// in a private view, closed after. The pod's identity comes from Klyx, never
// the model: a UID is used only when this conversation's ledger holds it
// from a read Klyx made; with several, the model must pass one of them, and
// nothing else is accepted. The whole text is scrubbed before each line is
// capped. Which identity the read used is said every time.

const (
	toolLogHistory      = "get_log_history"
	chatHistoryLinesMax = 200
)

var chatHistoryWindows = map[string]int64{"15m": 15 * 60, "1h": 60 * 60, "6h": 6 * 60 * 60}

// chatHistory reads a pod's log history for chat.
type chatHistory interface {
	history(ctx context.Context, req PodHistoryRequestDTO) LogHistoryPageDTO
}

// SetChatLogHistory gives chat its Loki read (AI A1 §3.6).
func SetChatLogHistory(c *ChatService, l *LogHistoryService) {
	if l != nil {
		c.logs = l
	}
}

// history reads one page in a view of its own, closed after.
func (s *LogHistoryService) history(ctx context.Context, req PodHistoryRequestDTO) LogHistoryPageDTO {
	req.View = fmt.Sprintf("chat-%d", s.chats.Add(1))
	req.Restart, req.Set, req.After, req.Skip = true, 0, 0, nil
	defer s.CloseView(req.View)
	p := s.readPodHistory(ctx, req)
	if err := ctx.Err(); err != nil {
		// the turn ended: said as such, never as a newer request
		return LogHistoryPageDTO{Error: readError(err)}
	}
	return p
}

type chatLogHistoryData struct {
	Identity string `json:"identity"` // by the pod's UID | by namespace and name
	Window   string `json:"window"`
	Start    string `json:"start,omitempty"`
	End      string `json:"end,omitempty"`
	Lines    int    `json:"lines"`
	Text     string `json:"text"` // newest first: "<time> <container> <line>"
}

func (t *chatTools) logHistory(ctx context.Context, ns, pod, uid, container, window string, tail int) ChatToolResult {
	target := ns + "/" + pod
	if t.history == nil {
		return t.failed(toolLogHistory, "loki", target, errors.New("Loki log history is not available in this session"))
	}
	held := []string{}
	if t.ledgerUIDs != nil {
		held = t.ledgerUIDs(ns, pod)
	}
	switch {
	case uid != "" && !contains(held, uid):
		return t.refuse(toolLogHistory, target, errors.New("a uid is accepted only when this conversation's own reads hold it for that pod; read the pod first, or omit uid"))
	case uid == "" && len(held) == 1:
		uid = held[0]
	case uid == "" && len(held) > 1:
		return t.refuse(toolLogHistory, target, fmt.Errorf("this conversation's reads hold %d pods named %s: pass one of their UIDs as uid (%s)", len(held), pod, strings.Join(held, ", ")))
	}
	page := t.history.history(ctx, PodHistoryRequestDTO{Namespace: ns, Pod: pod, UID: uid, Container: container,
		RangeSeconds: chatHistoryWindows[window], Mode: "auto"})
	if page.Error != "" {
		return t.failed(toolLogHistory, "loki", target, errors.New(page.Error))
	}
	data := chatLogHistoryData{Window: window, Start: page.Start, End: page.End}
	if page.Mode == "uid" {
		data.Identity = "by the pod's UID " + uid
	} else {
		data.Identity = "by namespace and name: every pod of that name in the window"
	}
	env := chatEnvelope{Tool: toolLogHistory, Source: "loki", ReadAt: t.stamp(), Complete: !page.More,
		Note: "Log history is Loki's, " + data.Identity + ". The text is scrubbed of recognized credential shapes; it is not guaranteed secret-free."}
	if page.NoStreams != "" {
		env.Note += " No streams matched this selection in the window (" + chatScrub(page.NoStreams, new(int)) + "): this says nothing about whether the pod logged."
	}
	var lines []string
	for i, l := range page.Lines {
		if i == tail {
			env.Complete = false
			break
		}
		lines = append(lines, strings.TrimSpace(l.TS+" "+l.Container)+" "+l.Line)
	}
	// the whole text is scrubbed first, so a credential split across lines
	// is still matched; then each line is capped (spec §4)
	text, n := ai.ScrubEvidence(strings.Join(lines, "\n"), len(strings.Join(lines, "\n"))+1)
	out := strings.Split(text, "\n")
	for i, l := range out {
		if r := []rune(l); len(r) > chatLogLineMax {
			out[i] = string(r[:chatLogLineMax]) + "…[line truncated]"
			n++
		}
	}
	if len(lines) == 0 {
		out = nil
	}
	data.Lines, data.Text = len(out), strings.Join(out, "\n")
	env.Data = data
	read := ChatRead{Tool: toolLogHistory, Target: target, Outcome: "completed", Redacted: n,
		Detail: fmt.Sprintf("%d lines, %s", data.Lines, strings.SplitN(data.Identity, ":", 2)[0])}
	if page.Mode == "uid" {
		read.Refs = []ChatRef{{GVR: "v1/pods", Namespace: ns, Name: pod, UID: uid}}
	} else {
		read.About = []ChatCheckSubject{byName("Pod", ns, pod)}
	}
	// when its lines were written, or, with none, the window it searched
	var times []string
	for i, l := range page.Lines {
		if i == tail {
			break
		}
		times = append(times, l.TS)
	}
	if len(times) == 0 {
		times = []string{page.Start, page.End}
	}
	if from, to, ok := observedSpan(times); ok {
		read.ObservedFrom, read.ObservedTo = from, to
	} else {
		read.ObservedUnknown = true
	}
	return t.finish(env, read)
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// ledgerPodUIDs are the UIDs this conversation's ledger holds for a pod
// name: from reads Klyx made (a reference carries the UID Klyx read).
func (r *turnRunner) ledgerPodUIDs(ns, name string) []string {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for _, ex := range r.c.exchanges {
		for _, rd := range ex.reads {
			if rd.Outcome != "completed" && rd.Outcome != "truncated" {
				continue
			}
			for _, ref := range rd.Refs {
				if ref.GVR == "v1/pods" && ref.Namespace == ns && ref.Name == name && ref.UID != "" && !seen[ref.UID] {
					seen[ref.UID] = true
					out = append(out, ref.UID)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// guardedHistory runs the turn's guard before Loki is read.
type guardedHistory struct {
	r *turnRunner
	h chatHistory
}

func (g guardedHistory) history(ctx context.Context, req PodHistoryRequestDTO) LogHistoryPageDTO {
	if err := g.r.check(); err != nil {
		return LogHistoryPageDTO{Error: readError(err)}
	}
	req.Cluster = g.r.c.cluster
	return g.h.history(ctx, req)
}
