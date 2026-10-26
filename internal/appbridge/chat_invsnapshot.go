package appbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/moomora/klyx/internal/ai"
	"github.com/moomora/klyx/internal/bounds"
	"github.com/moomora/klyx/internal/readfence"
)

// AI A1 §1.1: a provider without tools cannot ask for reads, so an
// investigation's first turn gets a fixed acquisition plan that Klyx runs
// from the anchor and the links its own results establish - never from
// model or cluster text. The document is at most 96 KiB, filled in the
// plan's order; what does not fit, or failed, is listed with its read ID.
// The model ends its answer with a fenced findings block, which Klyx
// labels as it labels report_findings.

const (
	invSnapshotMax  = bounds.InvSnapshotBytes
	invPlanObjects  = 3  // health-check objects followed
	invPlanPods     = 3  // failing pods per object
	invPlanLogTail  = 50 // lines of a previous container's log
	invSnapshotNote = "Snapshot: Klyx read a fixed set of evidence"
)

const invSnapshotAddendum = `

This is an investigation of one object, the first section of <cluster-data>, pinned by its UID. Klyx read a fixed set of evidence for it; you cannot ask for more.
- Investigate why it is failing from that evidence. Then end your answer with one fenced block, exactly:
` + "```findings" + `
{"findings":[{"type":"observation or cause","statement":"...","subject":{"kind":"...","namespace":"...","name":"..."},"cites":["r1","r1.c1"]}],"missing":["what you could not read"],"remedy":{"git":"a Git change, described","mitigation":"a mitigation the operator could run"}}
` + "```" + `
- Every finding cites the readId and check IDs it rests on. Klyx checks each citation and labels each finding itself: an observation is your reading, a cause is suspected, and a finding whose citations do not hold up is unsupported. Nothing is shown as proven.
- Evidence observed before the failure began does not establish it. A remedy is text only, proposed and never run.`

var findingsBlock = regexp.MustCompile("(?s)```findings[ \\t]*\\r?\\n(.*?)\\r?\\n```")

// actionsSnapshotAddendum frames a snapshot investigation's action
// proposal (AI A3 §2.1): one fenced actions block after the findings.
const actionsSnapshotAddendum = `
- When a mitigation is warranted, you may also end with one fenced actions block, after the findings block:
` + "```actions" + `
{"steps":[{"verb":"one of: %s","target":{"kind":"...","namespace":"...","name":"..."},"params":{"revision":4},"cites":["r1"],"why":"..."}]}
` + "```" + `
- At most 4 steps, each an existing Klyx verb on an object in <cluster-data> that Klyx read by UID; params only for scale (replicas) and rollout-undo (revision: the partner revision the rollout history established). Each step cites at least one read or check about its target. Klyx checks that each step is eligible to run, not that it is right; the operator reviews and confirms each step in order; you never run anything.`

var actionsBlock = regexp.MustCompile("(?s)```actions[ \\t]*\\r?\\n(.*?)\\r?\\n```")

// takeActions splits a snapshot answer into its prose and its last actions
// block.
func takeActions(answer string) (string, string, bool) {
	all := actionsBlock.FindAllStringSubmatchIndex(answer, -1)
	if len(all) == 0 {
		return answer, "", false
	}
	m := all[len(all)-1]
	prose := strings.TrimSpace(answer[:m[0]] + answer[m[1]:])
	return prose, answer[m[2]:m[3]], true
}

// takeFindings splits a snapshot answer into its prose and its last
// findings block.
func takeFindings(answer string) (string, string, bool) {
	all := findingsBlock.FindAllStringSubmatchIndex(answer, -1)
	if len(all) == 0 {
		return answer, "", false
	}
	m := all[len(all)-1]
	prose := strings.TrimSpace(answer[:m[0]] + answer[m[1]:])
	return prose, answer[m[2]:m[3]], true
}

// invPlan runs one acquisition plan within the investigation's read budget.
type invPlan struct {
	// compare is the workload a comparison set reads (A1 §1.1): the anchor,
	// or the first joined health-check object that is a workload
	compare  *[3]string
	r        *turnRunner
	ctx      context.Context
	exec     *chatTools
	sections []string
	included []string
	reads    int
	skipped  []string // steps the budget stopped
}

// step runs one read and keeps its envelope; it returns the result, or
// false when the budget or the turn stopped it.
func (p *invPlan) step(tool, args, label string) (ChatToolResult, bool, error) {
	if err := p.ctx.Err(); err != nil {
		return ChatToolResult{}, false, err
	}
	if p.reads >= invMaxToolCalls {
		p.skipped = append(p.skipped, label)
		return ChatToolResult{}, false, nil
	}
	if err := p.r.check(); err != nil {
		return ChatToolResult{}, false, err
	}
	p.reads++
	res := p.exec.as(p.r.nextReadID()).Run(p.ctx, tool, json.RawMessage(args))
	p.r.addRead(res.Read)
	p.r.recordFacts(res)
	p.r.evalRead(tool, json.RawMessage(args), res)
	p.sections = append(p.sections, string(res.JSON))
	p.included = append(p.included, res.Read.ID+" "+label+" ("+res.Read.Outcome+")")
	return res, true, p.r.aborted()
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// envelopeData decodes a result's data.
func envelopeData(res ChatToolResult, into any) bool {
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(res.JSON, &env) != nil || len(env.Data) == 0 {
		return false
	}
	return json.Unmarshal(env.Data, into) == nil
}

// failingPods are a workload's pods Klyx's verdict calls fault or degraded,
// at most invPlanPods, from the workload's controller-UID join.
func (p *invPlan) failingPods(kind, ns, name string) []chatPodRef {
	k, err := p.exec.resolve(p.ctx, kind, "apps")
	if err != nil {
		return nil
	}
	w, err := p.exec.workloadPods(p.ctx, k, ns, name)
	if err != nil {
		return nil
	}
	vs := p.exec.verdictsFor(p.ctx, podsGVRv1, new(int))
	var out []chatPodRef
	for _, rank := range []string{"fault", "degraded"} {
		for _, pod := range w.Pods {
			if vs == nil || len(out) == invPlanPods {
				break
			}
			if v, ok := vs.byUID[pod.UID]; ok && v.Kind == rank {
				out = append(out, pod)
			}
		}
	}
	return out
}

// podEvidence reads a failing pod, its previous container's log, and its
// history from Loki when the pod is gone and the ledger holds its UID.
func (p *invPlan) podEvidence(ns string, pod chatPodRef) error {
	res, ok, err := p.step(toolGet, mustJSON(map[string]any{"kind": "pod", "namespace": ns, "name": pod.Name}), "pod "+ns+"/"+pod.Name)
	if err != nil || !ok {
		return err
	}
	if res.Read.Outcome == "failed" && strings.HasPrefix(res.Read.Detail, "not found") {
		// gone: Loki's history, by the UID only if the ledger holds it (the
		// tool decides; the plan's own pod listing is not a ledger read)
		_, _, err = p.step(toolLogHistory, mustJSON(map[string]any{"namespace": ns, "pod": pod.Name, "window": "1h", "tail": invPlanLogTail}),
			"log history of "+ns+"/"+pod.Name)
		return err
	}
	_, _, err = p.step(toolLogs, mustJSON(map[string]any{"namespace": ns, "pod": pod.Name, "previous": true, "tail": invPlanLogTail}), "previous log of "+ns+"/"+pod.Name)
	return err
}

// run is the plan for the anchor's kind (A1 §1.1).
func (p *invPlan) run(kind, ns, name string, seed json.RawMessage) error {
	events := func(k, n, nsp string) error {
		_, _, err := p.step(toolEvents, mustJSON(map[string]any{"namespace": nsp, "kind": k, "name": n, "limit": 20}), "events of "+k+" "+nsp+"/"+n)
		return err
	}
	alerts := func(k, nsp, n string) error {
		_, _, err := p.step(toolAlerts, mustJSON(map[string]any{"kind": k, "namespace": nsp, "name": n}), "alerts for "+k+" "+nsp+"/"+n)
		return err
	}
	switch {
	case kind == "Kustomization":
		res, ok, err := p.step(toolUnit, mustJSON(map[string]any{"kind": "Kustomization", "namespace": ns, "name": name}), "the unit")
		if err != nil {
			return err
		}
		type object struct{ kind, ns, name string }
		var objects []object
		var unit chatUnitData
		if ok && envelopeData(res, &unit) && unit.HealthCheck != nil {
			for _, e := range unit.HealthCheck.Entries {
				if e.Current != nil && e.Current.UID != "" && len(objects) < invPlanObjects {
					objects = append(objects, object{e.Kind, e.Namespace, e.Name})
				}
			}
		}
		for _, o := range objects {
			if res, ok := workloadKindOf(o.kind); ok {
				if p.compare == nil {
					p.compare = &[3]string{res, o.ns, o.name}
				}
				for _, pod := range p.failingPods(res, o.ns, o.name) {
					if err := p.podEvidence(o.ns, pod); err != nil {
						return err
					}
				}
			}
		}
		if err := events("Kustomization", name, ns); err != nil {
			return err
		}
		for _, o := range objects {
			if err := events(o.kind, o.name, o.ns); err != nil {
				return err
			}
		}
		if _, _, err := p.step(toolChanges, mustJSON(map[string]any{"kind": "Kustomization", "namespace": ns, "name": name}), "Git changes"); err != nil {
			return err
		}
		// AI A1.1 §7: after git_changes, so the Git evidence keeps its place
		for _, o := range objects {
			if isWorkloadKind(o.kind) {
				if _, _, err := p.step(toolRollout, mustJSON(map[string]any{"kind": o.kind, "namespace": o.ns, "name": o.name}), "rollout history of "+o.kind+" "+o.ns+"/"+o.name); err != nil {
					return err
				}
			}
		}
		for _, o := range objects {
			if err := alerts(o.kind, o.ns, o.name); err != nil {
				return err
			}
		}
		_, _, err = p.step(toolDeps, mustJSON(map[string]any{"kind": "Kustomization", "namespace": ns, "name": name}), "dependencies")
		return err
	case isWorkloadKind(kind):
		p.compare = &[3]string{kind, ns, name}
		for _, pod := range p.failingPods(kind, ns, name) {
			if err := p.podEvidence(ns, pod); err != nil {
				return err
			}
		}
		if err := events(kind, name, ns); err != nil {
			return err
		}
		if _, _, err := p.step(toolRollout, mustJSON(map[string]any{"kind": kind, "namespace": ns, "name": name}), "rollout history"); err != nil {
			return err
		}
		if err := alerts(kind, ns, name); err != nil {
			return err
		}
		// its Flux owner, by the labels kustomize-controller writes
		var got chatGetData
		if envelopeData(ChatToolResult{JSON: seed}, &got) {
			ln, lns := got.Object.Labels["kustomize.toolkit.fluxcd.io/name"], got.Object.Labels["kustomize.toolkit.fluxcd.io/namespace"]
			if checkName("name", ln, true) == nil && checkName("namespace", lns, true) == nil {
				_, _, err := p.step(toolChanges, mustJSON(map[string]any{"kind": "Kustomization", "namespace": lns, "name": ln}), "Git changes of its Flux owner (by its labels)")
				return err
			}
		}
		return nil
	default:
		if err := events(kind, name, ns); err != nil {
			return err
		}
		if _, _, err := p.step(toolDeps, mustJSON(map[string]any{"kind": kind, "namespace": ns, "name": name}), "dependencies"); err != nil {
			return err
		}
		return alerts(kind, ns, name)
	}
}

// workloadKindOf maps a health-check entry's kind to a workload kind.
func workloadKindOf(kind string) (string, bool) {
	return kind, isWorkloadKind(kind)
}

func isWorkloadKind(kind string) bool { return chatMetricWorkloads[kind] }

// investigationSnapshot is the first turn of a snapshot investigation.
func (r *turnRunner) investigationSnapshot(ctx context.Context) (string, error) {
	if err := r.check(); err != nil {
		return "", err
	}
	exec := newChatTools(guardedConn{r}, r.s.now)
	r.equip(exec)
	p := &invPlan{r: r, ctx: ctx, exec: exec}
	ledger := r.ledger()
	if len(ledger) == 0 {
		return "", &chatError{msg: "the object to investigate was not read"}
	}
	kind, _, _ := strings.Cut(ledger[0].Target, " ")
	if err := p.run(kind, r.c.anchor.Namespace, r.c.anchor.Name, json.RawMessage(r.ex.seedData)); err != nil {
		return "", err
	}
	if len(exec.members) > 0 {
		if p.compare == nil {
			p.skipped = append(p.skipped, "the fleet comparison: no workload to compare")
		} else if _, _, err := p.step(toolFleetDiff, mustJSON(map[string]any{"kind": p.compare[0], "namespace": p.compare[1], "name": p.compare[2]}),
			"the fleet comparison of "+p.compare[0]+" "+p.compare[1]+"/"+p.compare[2]); err != nil {
			return "", err
		}
	}
	sections := append([]string{r.ex.seedData}, p.sections...)
	doc, dropped := invSnapshotDoc(sections, r.ex.question)
	r.evalSnapshotDoc(sections, dropped)
	included := append([]string{"the object investigated"}, p.included...)
	for _, s := range p.skipped {
		included = append(included, "not read: "+s)
	}
	r.s.mu.Lock()
	r.ex.snapshot = included
	r.ex.snapshotDropped = dropped
	r.ex.snapshotSkipped = p.skipped
	r.s.mu.Unlock()
	return r.completeInvestigation(ctx, doc)
}

// completeInvestigation sends a snapshot investigation's document and takes
// its findings block.
func (r *turnRunner) completeInvestigation(ctx context.Context, doc string) (string, error) {
	st, err := r.guard()
	if err != nil {
		return "", err
	}
	idx := r.startCall(st)
	system := fmt.Sprintf(chatSnapshotSystem, r.c.cluster) + invSnapshotAddendum
	if r.proposalsOn() {
		system += fmt.Sprintf(actionsSnapshotAddendum, strings.Join(matrixVerbs(), ", "))
	}
	res, err := st.Provider.Complete(readfence.WithWriteTracker(ctx), ai.Request{System: system, User: doc, Purpose: ai.PurposeChat})
	r.evalSnapshotSent(err)
	if err != nil {
		r.finishCall(idx, "", nil, callOutcome(err))
		return "", err
	}
	r.finishCall(idx, res.ModelUsed, nil, "completed")
	prose, block, found := takeFindings(res.Text)
	// an actions block is taken out of the prose whether or not proposals
	// are on; it is decided only when they are (AI A3 §6)
	prose, ablock, afound := takeActions(prose)
	ledger := r.ledger()
	a := anchorOf(r.c.anchor, ledger)
	var f FindingsDTO
	in, perr := parseFindings(json.RawMessage(block))
	r.evalSnapshotReport(found, found && perr == nil)
	switch {
	case !found:
		f = labelFindings(findingsInput{}, ledger, a)
		f.Note = "the answer carried no findings block"
	case perr != nil:
		f = labelFindings(findingsInput{}, ledger, a)
		f.Note = "the findings could not be read: " + chatScrub(perr.Error(), nil)
	default:
		f = labelFindings(in, ledger, a)
	}
	r.s.mu.Lock()
	for _, id := range r.ex.snapshotDropped {
		f.Missing = append(f.Missing, MissingDTO{From: "klyx", Read: id, Text: "read, but left out of the snapshot to fit its 96 KiB"})
	}
	for _, s := range r.ex.snapshotSkipped {
		f.Missing = append(f.Missing, MissingDTO{From: "klyx", Text: "not read: " + s})
	}
	f.Scope = invSnapshotNote
	r.ex.findings = &f
	if afound && r.proposalsOn() {
		// the snapshot's reads are all done: its ledger and facts are settled
		if ain, aerr := parseActions(json.RawMessage(ablock)); aerr != nil {
			r.ex.plan = &actionPlan{dto: ActionPlanDTO{Steps: []ActionStepDTO{}, Note: "the actions could not be read: " + chatScrub(aerr.Error(), nil)}}
		} else {
			p := validatePlan(ain, r.c.facts, ledgerLocked(r.c), a)
			r.ex.plan = &p
		}
	}
	r.s.mu.Unlock()
	return prose, nil
}

// invSnapshotDoc fills the document in the plan's order within
// invSnapshotMax; the sections left out are named by their read IDs.
func invSnapshotDoc(sections []string, question string) (string, []string) {
	var b strings.Builder
	var dropped []string
	head := "<cluster-data>\n"
	tail := "</cluster-data>\n\nQuestion: " + question
	b.WriteString(head)
	room := invSnapshotMax - len(head) - len(tail) - 256 // the dropped note
	for _, s := range sections {
		if len(s)+1 > room {
			var env struct {
				ReadID string `json:"readId"`
			}
			_ = json.Unmarshal([]byte(s), &env)
			dropped = append(dropped, env.ReadID)
			continue
		}
		room -= len(s) + 1
		b.WriteString(s)
		b.WriteString("\n")
	}
	if len(dropped) > 0 {
		fmt.Fprintf(&b, "{\"note\":\"left out to fit the snapshot: %s\"}\n", capText(strings.Join(dropped, ", "), 200))
	}
	b.WriteString(tail)
	return b.String(), dropped
}
