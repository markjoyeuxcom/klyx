// Package bounds holds the fixed safety ceilings Settings lists in
// Diagnostics (Settings S1 §3). Each is the constant its own package uses,
// so a changed value changes what Settings shows; a new ceiling joins the
// list by review. A leaf: it imports nothing of Klyx's.
package bounds

import "time"

// Chat (AI chat spec) and investigation (AI A1 §1) turn budgets.
const (
	ChatTurnDeadline  = 120 * time.Second
	ChatModelCalls    = 6
	ChatToolCalls     = 8
	ChatQuestionBytes = 4 * 1024
	ChatAnswerBytes   = 32 * 1024
	ChatRequestBytes  = 384 * 1024
	ChatSnapshotBytes = 48 * 1024

	InvTurnDeadline  = 300 * time.Second
	InvModelCalls    = 12
	InvToolCalls     = 24
	InvSnapshotBytes = 96 * 1024

	// Conversations is the conversation slots, chats and investigations
	// together.
	Conversations = 8
)

// The Git cache's caps (The Line slice 6 §7, §14).
const (
	GitRepoBytes    = 1 << 30
	GitTotalBytes   = 2 << 30
	GitContentBytes = 64 << 20
)

// The rows ledger's bound on a part's role list and each partition.
const RowsRoleCap = 50000

// One Alertmanager read's bounds (coverage S8a).
const (
	AlertsPerRead     = 5000
	AlertsBytes       = 8 << 20
	AlertsAppearances = 20000
)

// Bound is one ceiling as Settings lists it.
type Bound struct {
	ID    string // the registry entry is "bound." + ID
	Value int64
	Unit  string // calls, bytes, seconds, conversations, roles, alerts, groups
}

// List is every ceiling, in the order Diagnostics shows them.
func List() []Bound {
	return []Bound{
		{"chat.turnDeadline", int64(ChatTurnDeadline / time.Second), "seconds"},
		{"chat.modelCalls", ChatModelCalls, "calls"},
		{"chat.toolCalls", ChatToolCalls, "calls"},
		{"chat.questionBytes", ChatQuestionBytes, "bytes"},
		{"chat.answerBytes", ChatAnswerBytes, "bytes"},
		{"chat.requestBytes", ChatRequestBytes, "bytes"},
		{"chat.snapshotBytes", ChatSnapshotBytes, "bytes"},
		{"investigation.turnDeadline", int64(InvTurnDeadline / time.Second), "seconds"},
		{"investigation.modelCalls", InvModelCalls, "calls"},
		{"investigation.toolCalls", InvToolCalls, "calls"},
		{"investigation.snapshotBytes", InvSnapshotBytes, "bytes"},
		{"conversations", Conversations, "conversations"},
		{"git.repoBytes", GitRepoBytes, "bytes"},
		{"git.totalBytes", GitTotalBytes, "bytes"},
		{"git.contentBytes", GitContentBytes, "bytes"},
		{"rows.roleCap", RowsRoleCap, "roles"},
		{"alerts.perRead", AlertsPerRead, "alerts"},
		{"alerts.bytes", AlertsBytes, "bytes"},
		{"alerts.appearances", AlertsAppearances, "groups"},
	}
}
