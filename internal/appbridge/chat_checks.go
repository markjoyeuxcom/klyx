package appbridge

import (
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// AI A1 §2: a check is an observation Klyx evaluated itself, deterministically,
// on a read's data - never the model's words. Each names its subject and how
// Klyx holds its identity (by UID, or by name only, never upgraded), and keeps
// two times apart: when the evidence describes the world (observed) and when
// its condition began (onset), when the evidence says. A check that cannot be
// evaluated is not made; the read stays evidence the model may read.

// Check classes. Later stages add rollout, alert and metric.
const (
	checkVerdict     = "verdict"
	checkCondition   = "condition"
	checkHealthCheck = "healthcheck"
	checkDiff        = "diff"
	checkStatus      = "status"
	checkEvent       = "event"
)

// Identity strengths.
const (
	identityUID  = "uid"
	identityName = "name"
)

const (
	chatChecksMax      = 12
	chatCheckReasonMax = 64
)

// ChatCheckSubject is what a check is about, and how Klyx holds it.
type ChatCheckSubject struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	UID       string `json:"uid,omitempty"`
	Identity  string `json:"identity"`
}

// ChatCheck is one observation Klyx evaluated. ID is assigned when the read
// is finished: its read's ID and its place ("r4.c1").
type ChatCheck struct {
	ID        string           `json:"id"`
	Class     string           `json:"class"`
	Subject   ChatCheckSubject `json:"subject"`
	Statement string           `json:"statement"`
	Observed  string           `json:"observed,omitempty"`
	Onset     string           `json:"onset,omitempty"`
	Coverage  string           `json:"coverage,omitempty"`
}

func (s ChatCheckSubject) words() string {
	w := s.Kind + " " + strings.TrimPrefix(s.Namespace+"/"+s.Name, "/")
	if s.Identity == identityName {
		w += " (by name)"
	}
	return w
}

// checkText is cluster text inside a check: scrubbed and short, an
// identifier (a reason), never a message.
func checkText(s string, p *projector) string {
	return p.str(s, chatCheckReasonMax)
}

// verdictCheck states Klyx's own verdict for an object found by its UID.
func verdictCheck(subj ChatCheckSubject, v chatVerdict, p *projector) *ChatCheck {
	if v.Label == "" {
		return nil
	}
	st := fmt.Sprintf("Klyx's verdict for %s: %s", subj.words(), v.Label)
	if v.Reason != "" {
		st += " - " + checkText(v.Reason, p)
	}
	return &ChatCheck{Class: checkVerdict, Subject: subj, Statement: st, Observed: v.EvaluatedAt, Coverage: v.Coverage}
}

// Conditions whose status says a failure: a positive condition that is
// False, or a negative one that is True.
var (
	conditionPositive = map[string]bool{"Ready": true, "Available": true, "Healthy": true, "Synced": true, "Reconciled": true}
	conditionNegative = map[string]bool{"Stalled": true, "Degraded": true, "Failed": true}
)

// conditionChecks reads an object's failing conditions: type, status and
// reason, with the last transition as the onset; never the message.
func conditionChecks(u *unstructured.Unstructured, subj ChatCheckSubject, readAt string, p *projector) []ChatCheck {
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	var out []ChatCheck
	for _, c := range conds {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := m["type"].(string)
		status, _ := m["status"].(string)
		if !(conditionPositive[typ] && status == "False") && !(conditionNegative[typ] && status == "True") {
			continue
		}
		st := fmt.Sprintf("%s: %s=%s", subj.words(), checkText(typ, p), status)
		if reason, _ := m["reason"].(string); reason != "" {
			st += ", reason " + checkText(reason, p)
		}
		onset, _ := m["lastTransitionTime"].(string)
		out = append(out, ChatCheck{Class: checkCondition, Subject: subj, Statement: st, Observed: readAt, Onset: onset, Coverage: "live"})
	}
	return out
}

// Waiting reasons that say a container cannot run.
var failingWaiting = map[string]bool{
	"CrashLoopBackOff": true, "ImagePullBackOff": true, "ErrImagePull": true, "CreateContainerConfigError": true,
	"CreateContainerError": true, "InvalidImageName": true, "RunContainerError": true,
}

// podStatusChecks reads a pod's containers: a failing waiting reason, the
// restart count and the last termination's exit code and reason.
func podStatusChecks(u *unstructured.Unstructured, subj ChatCheckSubject, readAt string, p *projector) []ChatCheck {
	var out []ChatCheck
	for _, field := range []string{"initContainerStatuses", "containerStatuses"} {
		statuses, _, _ := unstructured.NestedSlice(u.Object, "status", field)
		for _, s := range statuses {
			m, ok := s.(map[string]any)
			if !ok {
				continue
			}
			name, _ := m["name"].(string)
			restarts, _, _ := unstructured.NestedInt64(m, "restartCount")
			waiting, _, _ := unstructured.NestedString(m, "state", "waiting", "reason")
			exit, hasExit, _ := unstructured.NestedInt64(m, "lastState", "terminated", "exitCode")
			lastReason, _, _ := unstructured.NestedString(m, "lastState", "terminated", "reason")
			if !failingWaiting[waiting] && restarts == 0 && !(hasExit && exit != 0) {
				continue
			}
			var parts []string
			if failingWaiting[waiting] {
				parts = append(parts, "waiting "+checkText(waiting, p))
			}
			if restarts > 0 {
				parts = append(parts, fmt.Sprintf("restarted %d times", restarts))
			}
			if hasExit {
				t := fmt.Sprintf("last terminated with exit code %d", exit)
				if lastReason != "" {
					t += " (" + checkText(lastReason, p) + ")"
				}
				parts = append(parts, t)
			}
			st := fmt.Sprintf("container %s of %s: %s", checkText(name, p), subj.words(), strings.Join(parts, "; "))
			out = append(out, ChatCheck{Class: checkStatus, Subject: subj, Statement: st, Observed: readAt, Coverage: "live"})
		}
	}
	return out
}

// eventChecks states warning events by object and reason, with their count
// and last occurrence; never the message. An event carrying its object's UID
// is by UID; one without is by name.
func eventChecks(evs []chatEvent) []ChatCheck {
	type key struct{ object, uid, ns, reason string }
	type agg struct {
		count int32
		last  string
	}
	seen := map[key]*agg{}
	var order []key
	for _, e := range evs {
		if e.Type != "Warning" || e.Reason == "" {
			continue
		}
		k := key{e.Object, e.ObjectUID, e.Namespace, e.Reason}
		a := seen[k]
		if a == nil {
			a = &agg{}
			seen[k] = a
			order = append(order, k)
		}
		n := e.Count
		if n == 0 {
			n = 1
		}
		a.count += n
		if e.LastSeen > a.last {
			a.last = e.LastSeen
		}
	}
	var out []ChatCheck
	for _, k := range order {
		kind, name, _ := strings.Cut(k.object, "/")
		subj := ChatCheckSubject{Kind: kind, Namespace: k.ns, Name: name, UID: k.uid, Identity: identityUID}
		if k.uid == "" {
			subj.Identity = identityName
		}
		a := seen[k]
		times := "times"
		if a.count == 1 {
			times = "time"
		}
		out = append(out, ChatCheck{Class: checkEvent, Subject: subj, Observed: a.last, Coverage: "live",
			Statement: fmt.Sprintf("Warning %s on %s (%d %s)", k.reason, subj.words(), a.count, times)})
	}
	return out
}

// capChecks keeps the first chatChecksMax, failures before facts: status,
// condition, health-check, diff and event checks first, then verdicts.
func capChecks(cs []ChatCheck) []ChatCheck {
	rank := map[string]int{checkStatus: 0, checkCondition: 1, checkHealthCheck: 2, checkDiff: 3, checkEvent: 4, checkVerdict: 5}
	sort.SliceStable(cs, func(i, j int) bool { return rank[cs[i].Class] < rank[cs[j].Class] })
	if len(cs) > chatChecksMax {
		cs = cs[:chatChecksMax]
	}
	return cs
}
