package observe

import "sort"

// Roles in triage order (health spec §1): lower is worse.
var roleRank = map[string]int{"fault": 0, "degraded": 1, "unknown": 2, "progressing": 3, "paused": 4, "ok": 5}

// RoleRank orders presentation roles worst first; an unrecognised role ranks
// with unknown (fail closed, never as ok).
func RoleRank(role string) int {
	if r, ok := roleRank[role]; ok {
		return r
	}
	return roleRank["unknown"]
}

// Attachment is one evaluation of an object against one parent - a route
// under one Gateway (spec §1.3).
type Attachment struct {
	UID, Namespace, Name string
	Parent               string // "namespace/name" of the parent
	Role, Label          string
	Included             bool
}

// Attached is one counted object reduced from its attachments: counted once
// by UID, with its worst attachment's role and parent (ties: parent name),
// Included when any attachment is.
type Attached struct {
	UID, Namespace, Name string
	Role, Label, Parent  string
	Included             bool
	Attachments          []Attachment // sorted by parent
}

// ReduceAttachments turns attachments into counted objects, sorted by
// namespace/name. Total and the role partition count these, never
// attachments.
func ReduceAttachments(atts []Attachment) []Attached {
	byUID := map[string]*Attached{}
	var order []string
	for _, a := range atts {
		e := byUID[a.UID]
		if e == nil {
			e = &Attached{UID: a.UID, Namespace: a.Namespace, Name: a.Name}
			byUID[a.UID] = e
			order = append(order, a.UID)
		}
		e.Attachments = append(e.Attachments, a)
		e.Included = e.Included || a.Included
	}
	out := make([]Attached, 0, len(order))
	for _, uid := range order {
		e := byUID[uid]
		sort.Slice(e.Attachments, func(i, j int) bool { return e.Attachments[i].Parent < e.Attachments[j].Parent })
		worst := e.Attachments[0]
		for _, a := range e.Attachments[1:] {
			if RoleRank(a.Role) < RoleRank(worst.Role) {
				worst = a
			}
		}
		e.Role, e.Label, e.Parent = worst.Role, worst.Label, worst.Parent
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out
}
