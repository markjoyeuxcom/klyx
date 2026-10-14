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

// Codex review of b547e60: Argo gives a non-empty sources list precedence;
// status records keep a singular source that may be empty or stale beside it.
func TestParseSourcesListWinsOverSingular(t *testing.T) {
	two := []interface{}{map[string]interface{}{"repoURL": "https://a"}, map[string]interface{}{"repoURL": "https://b"}}
	for name, single := range map[string]map[string]interface{}{
		"empty singular": {},
		"stale singular": {"repoURL": "https://stale"},
	} {
		block := func() map[string]interface{} { return map[string]interface{}{"source": single, "sources": two} }
		sync := block()
		sync["revisions"] = []interface{}{"r-a", "r-b"}
		sync["comparedTo"] = block()
		hist := block()
		hist["id"], hist["revisions"] = int64(1), []interface{}{"r-a", "r-b"}
		sr := block()
		sr["revisions"] = []interface{}{"r-a", "r-b"}
		a := Parse(app(block(), map[string]interface{}{
			"sync": sync, "history": []interface{}{hist},
			"operationState": map[string]interface{}{"phase": "Succeeded", "syncResult": sr},
		}))
		check := func(where string, ss []Source, revs []string) {
			if len(ss) != 2 || ss[0].RepoURL != "https://a" || ss[1].RepoURL != "https://b" {
				t.Errorf("%s %s: sources %+v", name, where, ss)
			}
			if revs != nil && (len(revs) != 2 || revs[1] != "r-b") {
				t.Errorf("%s %s: revisions %v", name, where, revs)
			}
		}
		check("spec", a.Sources, nil)
		check("compared", a.Compared.Sources, a.Compared.Revisions)
		check("history", a.History[0].Sources, a.History[0].Revisions.Revisions)
		check("syncResult", a.Operation.Sync.Sources, a.Operation.Sync.Revisions)
		if a.RepoURL != "https://a" || a.ExtraSources != 1 {
			t.Errorf("%s headline: %s +%d", name, a.RepoURL, a.ExtraSources)
		}
	}
}
