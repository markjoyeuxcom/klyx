package appbridge

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/moomora/klyx/internal/fleet"
)

// AI A1 §3.7 and A-C2: an investigation may compare its workload across an
// operator-fixed set of fleet members, chosen before its first question and
// fixed for its life. Each member's pinned connection and egress are checked
// before its read, and - once its data has been read - before every model
// send, since every later send carries it in the conversation's history. A
// member that disconnected or lost egress ends the investigation.

const (
	toolFleetDiff     = "fleet_difference"
	chatComparisonMax = 5
)

func fleetDiffToolDef() ChatToolDef {
	return ChatToolDef{toolFleetDiff, "Compare one workload across this investigation's comparison clusters and the investigated one: found or not, replicas, images and resources, a pod-spec hash, and its Flux owner's applied revision, with each member's differences from the investigated cluster.", obj(map[string]any{
		"kind":      map[string]any{"type": "string", "enum": []string{"Deployment", "StatefulSet", "DaemonSet"}, "description": "The workload's kind."},
		"namespace": strProp("Namespace."),
		"name":      strProp("Exact name."),
		"clusters":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional: some of the comparison clusters; all when omitted."},
	}, "kind", "namespace", "name")}
}

// member is one comparison cluster, pinned to its connection when chosen.
type member struct {
	cluster string
	conn    ChatConn
}

// SetComparison fixes an investigation's comparison set before its first
// question (A-C2). Each member is another connected cluster that egress may
// reach under the conversation's AI settings.
func (s *ChatService) SetComparison(cluster, conversationID string, members []string) ChatStateDTO {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.convLocked(cluster, conversationID)
	notice := func(msg string) ChatStateDTO {
		st := s.stateLocked(c)
		st.Notice = msg
		return st
	}
	switch {
	case c == nil:
		return s.obsoleteLocked(cluster, invKey(cluster), "that investigation has ended")
	case c.kind != kindInvestigation:
		return notice("only an investigation compares clusters")
	case c.ended != "":
		return notice("this investigation has ended; start a new one")
	case len(c.exchanges) > 0 || c.active != nil:
		return notice("the comparison set is fixed once the investigation has begun")
	case len(members) > chatComparisonMax:
		return notice(fmt.Sprintf("at most %d clusters to compare", chatComparisonMax))
	}
	st := s.ai.load()
	if st.Rev != c.rev {
		return notice("AI settings changed; start a new investigation")
	}
	seen := map[string]bool{}
	var set []member
	for _, m := range members {
		switch {
		case m == "" || seen[m]:
			continue
		case m == cluster:
			return notice("the investigated cluster is not a comparison member")
		}
		seen[m] = true
		if refusal := s.refusal(st, m); refusal != "" {
			return notice(m + ": " + refusal)
		}
		conn, usable := s.state(m)
		if conn == nil || !usable {
			return notice(m + " is not connected")
		}
		set = append(set, member{cluster: m, conn: conn})
	}
	c.members = set
	s.version++
	return s.stateLocked(c)
}

// memberProblem is why a member may no longer be read or sent, "" when it
// may: its pinned connection is the registry's and usable, and egress to it
// is allowed under the turn's AI settings.
func (s *ChatService) memberProblem(st *AIState, m member) string {
	conn, usable := s.state(m.cluster)
	if conn == nil || conn != m.conn || !usable {
		return "the comparison cluster " + m.cluster + " disconnected; start a new investigation"
	}
	if refusal := s.refusal(st, m.cluster); refusal != "" {
		return "the comparison cluster " + m.cluster + ": " + refusal
	}
	return ""
}

// chatMember is one comparison member as a tool reads it: its read runs the
// turn's guard and the member's own fence first.
type chatMember struct {
	cluster string
	compare func(ctx context.Context, kind, ns, name string) (fleet.WorkloadCompareInfo, error)
}

type chatContainerDiff struct {
	Name      string `json:"name"`
	Image     string `json:"image"`
	Resources string `json:"resources,omitempty"`
}

type chatClusterView struct {
	Cluster       string              `json:"cluster"`
	Role          string              `json:"role"` // home | member
	Found         bool                `json:"found"`
	Error         string              `json:"error,omitempty"`
	Desired       int32               `json:"desiredReplicas,omitempty"`
	Ready         int32               `json:"readyReplicas,omitempty"`
	Containers    []chatContainerDiff `json:"containers,omitempty"`
	SpecHash      string              `json:"specHash,omitempty"`
	Owner         string              `json:"owner,omitempty"`
	OwnerRevision string              `json:"ownerRevision,omitempty"`
	OwnerNote     string              `json:"ownerRevisionNote,omitempty"`
	Differences   []string            `json:"differencesFromHome,omitempty"`
}

type chatFleetDiffData struct {
	Kind     string            `json:"kind"`
	Clusters []chatClusterView `json:"clusters"`
	Note     string            `json:"note"`
}

func clusterView(cluster, role string, info fleet.WorkloadCompareInfo, err error, p *projector) chatClusterView {
	v := chatClusterView{Cluster: cluster, Role: role}
	if err != nil {
		v.Error = chatScrub(readError(err), &p.redacted)
		return v
	}
	v.Found, v.Desired, v.Ready, v.SpecHash = info.Found, info.DesiredReplicas, info.ReadyReplicas, info.SpecHash
	for _, c := range info.Containers {
		v.Containers = append(v.Containers, chatContainerDiff{Name: p.str(c.Name, chatCheckReasonMax), Image: p.str(c.Image, chatStringMax), Resources: p.str(c.Resources, chatStringMax)})
	}
	if info.OwnerName != "" {
		v.Owner = p.str(strings.TrimSpace(info.OwnerKind+" "+strings.TrimPrefix(info.OwnerNamespace+"/"+info.OwnerName, "/")), chatStringMax)
	}
	v.OwnerRevision, v.OwnerNote = p.str(info.OwnerRevision, chatStringMax), p.str(info.OwnerRevisionNote, chatStringMax)
	return v
}

// differences says how a member's view differs from home's.
func differences(home, m chatClusterView) []string {
	switch {
	case home.Error != "" || m.Error != "":
		return []string{"not compared: a read failed"}
	case home.Found != m.Found:
		if m.Found {
			return []string{"present here, absent on home"}
		}
		return []string{"absent here, present on home"}
	case !m.Found:
		return nil
	}
	var out []string
	if home.Desired != m.Desired {
		out = append(out, fmt.Sprintf("desired replicas: home %d, here %d", home.Desired, m.Desired))
	}
	hc := map[string]chatContainerDiff{}
	for _, c := range home.Containers {
		hc[c.Name] = c
	}
	for _, c := range m.Containers {
		h, ok := hc[c.Name]
		switch {
		case !ok:
			out = append(out, "container "+c.Name+": only here")
		case h.Image != c.Image:
			out = append(out, fmt.Sprintf("container %s image: home %s, here %s", c.Name, h.Image, c.Image))
		case h.Resources != c.Resources:
			out = append(out, fmt.Sprintf("container %s resources: home %s, here %s", c.Name, h.Resources, c.Resources))
		}
		delete(hc, c.Name)
	}
	for name := range hc {
		out = append(out, "container "+name+": only on home")
	}
	if home.SpecHash != m.SpecHash {
		out = append(out, "the pod specs differ (spec hash)")
	}
	switch {
	case home.OwnerRevision != "" && m.OwnerRevision != "" && home.OwnerRevision != m.OwnerRevision:
		out = append(out, fmt.Sprintf("owner's applied revision: home %s, here %s", home.OwnerRevision, m.OwnerRevision))
	case (home.OwnerRevision == "") != (m.OwnerRevision == ""):
		out = append(out, "owner's applied revision: not resolved on both, not compared")
	}
	sort.Strings(out)
	return out
}

func (t *chatTools) fleetDiff(ctx context.Context, kind, ns, name string, only []string) ChatToolResult {
	target := kind + " " + ns + "/" + name
	if len(t.members) == 0 {
		return t.refuse(toolFleetDiff, target, errors.New("this investigation has no comparison set"))
	}
	set := map[string]bool{}
	for _, m := range t.members {
		set[m.cluster] = true
	}
	for _, c := range only {
		if !set[c] {
			return t.refuse(toolFleetDiff, target, fmt.Errorf("%s is not in this investigation's comparison set", label(c)))
		}
	}
	p := &projector{}
	home, err := t.conn.WorkloadCompare(ctx, kind, ns, name)
	hv := clusterView(t.cluster, "home", home, err, p)
	data := chatFleetDiffData{Kind: kind, Clusters: []chatClusterView{hv},
		Note: "Each cluster's workload by kind, namespace and name (by name); the spec hash covers the pod spec only. Differences are each member's from the investigated cluster."}
	env := chatEnvelope{Tool: toolFleetDiff, Source: "api", ReadAt: t.stamp(), Complete: hv.Error == ""}
	for _, m := range t.members {
		if len(only) > 0 && !contains(only, m.cluster) {
			continue
		}
		info, err := m.compare(ctx, kind, ns, name)
		var ended *endError
		if errors.As(err, &ended) {
			return t.failed(toolFleetDiff, "api", target, err)
		}
		mv := clusterView(m.cluster, "member", info, err, p)
		mv.Differences = differences(hv, mv)
		if mv.Error != "" {
			env.Complete = false
		}
		data.Clusters = append(data.Clusters, mv)
	}
	env.Data = data
	return t.finish(env, ChatRead{Tool: toolFleetDiff, Target: target, Outcome: "completed", Redacted: p.redacted,
		Refs:   []ChatRef{{GVR: "apps/v1/" + strings.ToLower(kind) + "s", Namespace: ns, Name: name}},
		Detail: fmt.Sprintf("%d clusters", len(data.Clusters))})
}
