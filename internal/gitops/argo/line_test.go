package argo

import "testing"

// The Line §3.2: compared revisions pair with sync.comparedTo's sources,
// history entries with their own, never with today's spec order.
func TestParseRevisionFactsPairedWithTheirOwnSources(t *testing.T) {
	src := func(repo string) map[string]interface{} {
		return map[string]interface{}{"repoURL": repo, "path": "p", "targetRevision": "HEAD"}
	}
	a := Parse(app(
		map[string]interface{}{"sources": []interface{}{src("https://b"), src("https://a")}}, // reordered since
		map[string]interface{}{
			"sync": map[string]interface{}{"status": "OutOfSync", "revisions": []interface{}{"r-a2", "r-b2"},
				"comparedTo": map[string]interface{}{"sources": []interface{}{src("https://a"), src("https://b")}}},
			"history": []interface{}{
				map[string]interface{}{"id": int64(1), "revisions": []interface{}{"r-a0", "r-b0"}, "sources": []interface{}{src("https://a"), src("https://b")}, "deployedAt": "2026-10-13T10:00:00Z"},
				map[string]interface{}{"id": int64(2), "revisions": []interface{}{"r-a1", "r-b1"}, "sources": []interface{}{src("https://a"), src("https://b")}, "deployedAt": "2026-10-14T10:00:00Z"},
			},
			"operationState": map[string]interface{}{"phase": "Failed", "message": "sync failed", "startedAt": "2026-10-14T11:00:00Z",
				"syncResult": map[string]interface{}{"revisions": []interface{}{"r-a2", "r-b2"}, "sources": []interface{}{src("https://a"), src("https://b")}}},
		},
	))
	if len(a.Sources) != 2 || a.Sources[0].RepoURL != "https://b" {
		t.Fatalf("spec sources as written: %+v", a.Sources)
	}
	if a.Compared.Sources[0].RepoURL != "https://a" || a.Compared.Revisions[0] != "r-a2" {
		t.Fatalf("compared pairs with comparedTo, not spec: %+v", a.Compared)
	}
	if len(a.History) != 2 || a.History[0].ID != 2 || a.History[0].Revisions.Revisions[0] != "r-a1" || a.History[0].Sources[0].RepoURL != "https://a" || a.History[0].DeployedAt.IsZero() {
		t.Fatalf("history newest first, each with its own sources: %+v", a.History)
	}
	if a.Operation.Phase != "Failed" || a.Operation.Sync.Revisions[1] != "r-b2" || a.Operation.StartedAt.IsZero() {
		t.Fatalf("operation attempt: %+v", a.Operation)
	}
}

func TestParseSingleSourceAndCappedHistory(t *testing.T) {
	var hist []interface{}
	for i := 1; i <= 8; i++ {
		hist = append(hist, map[string]interface{}{"id": int64(i), "revision": "r", "source": map[string]interface{}{"repoURL": "https://a"}})
	}
	a := Parse(app(map[string]interface{}{"source": map[string]interface{}{"repoURL": "https://a", "chart": "podinfo", "targetRevision": "6.*"}},
		map[string]interface{}{"sync": map[string]interface{}{"status": "Synced", "revision": "6.5.0", "comparedTo": map[string]interface{}{"source": map[string]interface{}{"repoURL": "https://a"}}}, "history": hist}))
	if len(a.Sources) != 1 || a.Sources[0].TargetRevision != "6.*" || a.Compared.Revisions[0] != "6.5.0" {
		t.Fatalf("single source and compared: %+v %+v", a.Sources, a.Compared)
	}
	if len(a.History) != HistoryMax || a.History[0].ID != 8 || a.History[len(a.History)-1].ID != 4 {
		t.Fatalf("history capped newest first: %+v", a.History)
	}
	if b := Parse(app(map[string]interface{}{"source": map[string]interface{}{"repoURL": "https://a"}}, map[string]interface{}{})); b.History != nil || len(b.Compared.Revisions) != 0 {
		t.Fatalf("no history is no history, never an invented deployment: %+v", b)
	}
}
