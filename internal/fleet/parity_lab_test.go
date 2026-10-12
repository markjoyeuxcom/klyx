package fleet_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/appbridge"
	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/observe"
)

// The 1c slice 6 coverage-parity gate (docs/superpowers/specs/
// 2026-10-12-klyx-health-1c-slice6-parity.md), the Go stage: the production
// connection against the owned parity cluster, one case at a time on a
// healthy baseline, each case settled against an oracle declared on the
// fixture's objects and captured for the frontend stage.
//
//	scripts/lab/klyx-parity.sh up
//	KLYX_PARITY_CONTEXT=kind-klyx-parity KLYX_PARITY_DIR=<dir> KLYX_PARITY_OUT=<dir> \
//	  go test ./internal/fleet -run TestParityGate -v -count=1 -timeout 60m
//
// It refuses any target but the cluster the script created and marked.

const (
	parityContext = "kind-klyx-parity"
	parityReprobe = 10 * time.Second // the disclosed cadence-only override (spec §3)
	paritySettle  = 90 * time.Second
	parityRestart = 120 * time.Second
)

type parityRun struct {
	ID        string    `json:"id"`
	Commit    string    `json:"commit"`
	Dirty     bool      `json:"dirty"`
	StartedAt time.Time `json:"startedAt"`
	Reprobe   string    `json:"reprobe"`
}

type parityLab struct {
	t      *testing.T
	dir    string // the script's state: kubeconfig, ownership marker
	out    string
	script string
	run    parityRun
	report []string
}

func (l *parityLab) sh(args ...string) string {
	l.t.Helper()
	cmd := exec.Command("bash", append([]string{l.script}, args...)...)
	cmd.Env = append(os.Environ(), "KLYX_PARITY_DIR="+l.dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		l.t.Fatalf("klyx-parity.sh %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// kubectl reads the parity cluster for the oracle - independent of Klyx's
// code paths (spec §1).
func (l *parityLab) kubectl(args ...string) []byte {
	l.t.Helper()
	cmd := exec.Command("kubectl", append([]string{"--kubeconfig", filepath.Join(l.dir, "kubeconfig"), "--context", parityContext}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		l.t.Fatalf("kubectl %s: %v", strings.Join(args, " "), err)
	}
	return out
}

// ---- the sink: the latest summary and every part's latest rows ----------

type paritySink struct {
	mu   sync.Mutex
	sum  observe.Summary
	ok   bool
	rows map[string]observe.Rows
}

func (s *paritySink) Summary(x observe.Summary) { s.mu.Lock(); s.sum, s.ok = x, true; s.mu.Unlock() }
func (s *paritySink) Rows(r observe.Rows) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rows == nil {
		s.rows = map[string]observe.Rows{}
	}
	s.rows[r.Task+"/"+r.Part.ID] = r
}
func (s *paritySink) state() (observe.Summary, map[string]observe.Rows, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := make(map[string]observe.Rows, len(s.rows))
	for k, v := range s.rows {
		rows[k] = v
	}
	return s.sum, rows, s.ok
}

// ---- the oracle (spec §1) ------------------------------------------------

// parityPart is one expected part: its classifier and the kinds it counts
// (summary kind name -> kubectl resource).
type parityPart struct {
	Task, Part, Classifier string
	Counted                []string
}

var parityParts = []parityPart{
	{"workloads", "workloads", "verdict", []string{"deployments", "statefulsets", "daemonsets"}},
	{"pods", "pods", "verdict", []string{"pods"}},
	{"nodes", "nodes", "verdict", []string{"nodes"}},
	{"events", "warnings", "evidence", nil},
	{"helm", "releases", "native", nil},
	{"flux", "reconciliations", "native", []string{"kustomizations", "helmreleases"}},
	{"flux", "sources", "native", []string{"gitrepositories", "ocirepositories", "buckets", "helmrepositories", "helmcharts"}},
	{"argo", "applications", "native", []string{"applications"}},
	{"argo", "applicationsets", "native", []string{"applicationsets"}},
	{"certs", "certificates", "native", []string{"certificates"}},
	{"certs", "issuers", "native", []string{"issuers", "clusterissuers"}},
	{"eso", "externalsecrets", "native", []string{"externalsecrets"}},
	{"eso", "stores", "native", []string{"secretstores", "clustersecretstores"}},
	{"gateway", "gateways", "native", []string{"gateways"}},
	{"gateway", "routes", "native", []string{"httproutes"}},
	{"karpenter", "nodeclaims", "native", []string{"nodeclaims"}},
	{"karpenter", "nodepools", "native", []string{"nodepools"}},
}

var parityResource = map[string]string{
	"deployments": "deployments.apps", "statefulsets": "statefulsets.apps", "daemonsets": "daemonsets.apps",
	"pods": "pods", "nodes": "nodes",
	"kustomizations": "kustomizations.kustomize.toolkit.fluxcd.io", "helmreleases": "helmreleases.helm.toolkit.fluxcd.io",
	"gitrepositories": "gitrepositories.source.toolkit.fluxcd.io", "ocirepositories": "ocirepositories.source.toolkit.fluxcd.io",
	"buckets": "buckets.source.toolkit.fluxcd.io", "helmrepositories": "helmrepositories.source.toolkit.fluxcd.io",
	"helmcharts":   "helmcharts.source.toolkit.fluxcd.io",
	"applications": "applications.argoproj.io", "applicationsets": "applicationsets.argoproj.io",
	"certificates": "certificates.cert-manager.io", "issuers": "issuers.cert-manager.io", "clusterissuers": "clusterissuers.cert-manager.io",
	"externalsecrets": "externalsecrets.external-secrets.io", "secretstores": "secretstores.external-secrets.io",
	"clustersecretstores": "clustersecretstores.external-secrets.io",
	"gateways":            "gateways.gateway.networking.k8s.io", "httproutes": "httproutes.gateway.networking.k8s.io",
	"nodeclaims": "nodeclaims.karpenter.sh", "nodepools": "nodepools.karpenter.sh",
}

// partExpect is the oracle for one part at one case.
type partExpect struct {
	Task       string            `json:"task"`
	Part       string            `json:"part"`
	Classifier string            `json:"classifier"`
	Counted    []string          `json:"counted"`
	Roles      observe.Counts    `json:"roles"` // Total included
	Included   int               `json:"included"`
	UIDs       []string          `json:"uids"`
	Coverage   map[string]string `json:"coverage"` // counted kind -> live | stale | unreadable:<class>
}

// caseExpect is one case's declared state.
type caseExpect struct {
	Case        string            `json:"case"`
	Parts       []partExpect      `json:"parts"`
	Controllers map[string]string `json:"controllers"` // task -> "" | not ready | unknown
	Connection  string            `json:"connection"`  // a prefix of the summary's connection word
	Notes       []string          `json:"notes,omitempty"`
}

type parityObj struct {
	Metadata struct {
		UID         string            `json:"uid"`
		Annotations map[string]string `json:"annotations"`
		Name        string            `json:"name"`
		Namespace   string            `json:"namespace"`
	} `json:"metadata"`
}

func addRole(c *observe.Counts, role string) {
	c.Total++
	switch role {
	case "fault":
		c.Fault++
	case "degraded":
		c.Degraded++
	case "unknown":
		c.Unknown++
	case "progressing":
		c.Progressing++
	case "paused":
		c.Paused++
	default:
		c.OK++
	}
}

// oracle builds a case's expectations from the objects' declarations.
// readable: kinds the case's identity may read (nil = all); coverage
// overrides a kind's expected coverage.
func (l *parityLab) oracle(name string, coverage map[string]string, controllers map[string]string) caseExpect {
	ex := caseExpect{Case: name, Controllers: map[string]string{}, Connection: "connected"}
	for _, t := range []string{"flux", "argo", "certs", "eso", "gateway", "karpenter"} {
		ex.Controllers[t] = controllers[t]
	}
	for _, p := range parityParts {
		pe := partExpect{Task: p.Task, Part: p.Part, Classifier: p.Classifier, Counted: p.Counted, Coverage: map[string]string{}}
		for _, kind := range p.Counted {
			cov := "live"
			if c, ok := coverage[kind]; ok {
				cov = c
			}
			pe.Coverage[kind] = cov
			if strings.HasPrefix(cov, "unreadable") {
				continue // only what the identity could read is compared (spec §1)
			}
			var list struct{ Items []parityObj }
			if err := json.Unmarshal(l.kubectl("get", parityResource[kind], "-A", "-o", "json"), &list); err != nil {
				l.t.Fatalf("oracle %s: %v", kind, err)
			}
			for _, o := range list.Items {
				role, included := o.Metadata.Annotations["klyx.dev/parity-role"], o.Metadata.Annotations["klyx.dev/parity-included"] == "true"
				if role == "" {
					role = "ok"
				}
				addRole(&pe.Roles, role)
				if included {
					pe.Included++
				}
				pe.UIDs = append(pe.UIDs, o.Metadata.UID)
			}
		}
		sort.Strings(pe.UIDs)
		ex.Parts = append(ex.Parts, pe)
	}
	return ex
}

// ---- the settled barrier (spec §3) ---------------------------------------

func findPart(sum observe.Summary, task, part string) (*observe.Task, *observe.Part) {
	for i := range sum.Tasks {
		if sum.Tasks[i].ID != task {
			continue
		}
		for j := range sum.Tasks[i].Parts {
			if sum.Tasks[i].Parts[j].ID == part {
				return &sum.Tasks[i], &sum.Tasks[i].Parts[j]
			}
		}
		return &sum.Tasks[i], nil
	}
	return nil, nil
}

func roleCounts(p *observe.Part) (observe.Counts, int, bool) {
	switch {
	case p.Counts != nil:
		return *p.Counts, -1, true
	case p.Native != nil:
		r := p.Native.Roles
		r.Total = p.Native.Total
		return r, p.Native.Included, true
	}
	return observe.Counts{}, 0, false
}

// mismatches compares one summary (and the subscribed rows) with a case's
// oracle: every expected part, its coverage, its counts, every controller,
// the connection - and the rows at the same incarnation and Ver.
func mismatches(ex caseExpect, sum observe.Summary, rows map[string]observe.Rows) []string {
	var bad []string
	if !strings.HasPrefix(sum.Connection, ex.Connection) {
		bad = append(bad, fmt.Sprintf("connection %q, want %q", sum.Connection, ex.Connection))
	}
	for task, want := range ex.Controllers {
		t, _ := findPart(sum, task, "")
		got := ""
		if t != nil && t.Controller != nil {
			got = t.Controller.State
		}
		if got != want {
			bad = append(bad, fmt.Sprintf("%s controller %q, want %q", task, got, want))
		}
	}
	for _, pe := range ex.Parts {
		key := pe.Task + "/" + pe.Part
		_, p := findPart(sum, pe.Task, pe.Part)
		if p == nil {
			bad = append(bad, key+" missing from the summary")
			continue
		}
		if p.Classifier != pe.Classifier {
			bad = append(bad, fmt.Sprintf("%s classifier %q, want %q", key, p.Classifier, pe.Classifier))
		}
		counted := map[string]observe.KindCoverage{}
		for _, k := range p.Kinds {
			if k.Role == "counted" {
				counted[k.Kind] = k
			}
		}
		for _, kind := range pe.Counted {
			k, ok := counted[kind]
			if !ok {
				bad = append(bad, fmt.Sprintf("%s does not count %s", key, kind))
				continue
			}
			got := string(k.Coverage)
			if k.Class != "" && k.Coverage == observe.Unreadable {
				got += ":" + k.Class
			}
			if got != pe.Coverage[kind] {
				bad = append(bad, fmt.Sprintf("%s %s coverage %q, want %q", key, kind, got, pe.Coverage[kind]))
			}
		}
		if pe.Classifier == "evidence" {
			if p.Evidence == nil {
				bad = append(bad, key+" has no evidence count")
			}
			continue
		}
		got, inc, ok := roleCounts(p)
		if !ok {
			bad = append(bad, key+" has no counts")
			continue
		}
		if got != pe.Roles {
			bad = append(bad, fmt.Sprintf("%s roles %+v, want %+v", key, got, pe.Roles))
		}
		if inc >= 0 && inc != pe.Included {
			bad = append(bad, fmt.Sprintf("%s included %d, want %d", key, inc, pe.Included))
		}
		if r, ok := rows[key]; ok {
			if r.Incarnation != sum.Incarnation || r.Part.Ver != p.Ver {
				bad = append(bad, fmt.Sprintf("%s rows at incarnation %d ver %d, summary %d ver %d", key, r.Incarnation, r.Part.Ver, sum.Incarnation, p.Ver))
			} else if rc, rinc, _ := roleCounts(&r.Part); rc != got || rinc != inc {
				bad = append(bad, fmt.Sprintf("%s rows %+v/%d, summary %+v/%d", key, rc, rinc, got, inc))
			} else if uids := rowUIDs(r); strings.Join(uids, ",") != strings.Join(pe.UIDs, ",") {
				bad = append(bad, fmt.Sprintf("%s counted members %v, want %v", key, uids, pe.UIDs))
			}
		} else {
			bad = append(bad, key+" has no rows")
		}
	}
	return bad
}

// onlyRowsMissing: a re-check without the rows map flags only "has no rows".
func onlyRowsMissing(bad []string) bool {
	for _, b := range bad {
		if !strings.HasSuffix(b, " has no rows") {
			return false
		}
	}
	return true
}

// rowUIDs is a part's counted membership as its rows carry it.
func rowUIDs(r observe.Rows) []string {
	b, err := json.Marshal(r.Rows)
	if err != nil {
		return nil
	}
	var env struct {
		Rows []struct{ Ref struct{ UID string } }
	}
	if json.Unmarshal(b, &env) != nil {
		return nil
	}
	out := make([]string, 0, len(env.Rows))
	for _, x := range env.Rows {
		out = append(out, x.Ref.UID)
	}
	sort.Strings(out)
	return out
}

// settle waits for the barrier: every mismatch gone in one summary. The
// oracle is re-read every 2s, so both sides describe the same cluster
// state (a Recreate rollout's old pod is gone from both, never one).
func (l *parityLab) settle(sink *paritySink, oracle func() caseExpect, limit time.Duration) (caseExpect, observe.Summary, map[string]observe.Rows, time.Duration) {
	l.t.Helper()
	t0 := time.Now()
	ex, read := oracle(), time.Now()
	var last []string
	for end := t0.Add(limit); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		if time.Since(read) > 2*time.Second {
			ex, read = oracle(), time.Now()
		}
		sum, rows, ok := sink.state()
		if !ok {
			continue
		}
		if last = mismatches(ex, sum, rows); len(last) == 0 {
			return ex, sum, rows, time.Since(t0)
		}
	}
	l.t.Fatalf("case %s did not settle within %v:\n  %s", ex.Case, limit, strings.Join(last, "\n  "))
	return caseExpect{}, observe.Summary{}, nil, 0
}

// ---- capture (spec §3) ---------------------------------------------------

type parityCapture struct {
	Run     parityRun            `json:"run"`
	Case    string               `json:"case"`
	Expect  caseExpect           `json:"expect"`
	Summary observe.Summary      `json:"summary"`
	Cluster appbridge.ClusterDTO `json:"cluster"`
	Rows    []observe.Rows       `json:"rows"`
	Settled string               `json:"settled"`
}

func (l *parityLab) capture(name string, ex caseExpect, conn *fleet.ClusterConn, cc config.ClusterConfig, sum observe.Summary, rows map[string]observe.Rows, took time.Duration) {
	l.t.Helper()
	c := parityCapture{Run: l.run, Case: name, Expect: ex, Summary: sum, Cluster: appbridge.ToDTO(conn.Snapshot(), cc, time.Now()), Settled: took.Round(time.Millisecond).String()}
	keys := make([]string, 0, len(rows))
	for k := range rows {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		c.Rows = append(c.Rows, rows[k])
	}
	b, err := json.MarshalIndent(c, "", " ")
	if err != nil {
		l.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(l.out, name+".json"), b, 0o600); err != nil {
		l.t.Fatal(err)
	}
	l.report = append(l.report, fmt.Sprintf("  %-4s settled in %v (incarnation %d, seq %d)", name, took.Round(time.Millisecond), sum.Incarnation, sum.Seq))
}

// ---- connections ----------------------------------------------------------

type parityConn struct {
	conn   *fleet.ClusterConn
	sink   *paritySink
	cc     config.ClusterConfig
	cancel context.CancelFunc
	unsub  []func()
}

func (l *parityLab) connect(kubeconfig string) *parityConn {
	l.t.Helper()
	cc := config.ClusterConfig{Name: parityContext, Context: parityContext, Kubeconfig: kubeconfig}
	conn, err := fleet.DefaultConnFactory(clock.Real{})(cc)
	if err != nil {
		l.t.Fatal(err)
	}
	c := conn.(*fleet.ClusterConn)
	pc := &parityConn{conn: c, sink: &paritySink{}, cc: cc}
	c.SetHealthSink(pc.sink)
	ctx, cancel := context.WithCancel(context.Background())
	pc.cancel = cancel
	c.Start(ctx)
	// every part's rows subscribed, as the lenses will be
	waitParity(l.t, 120*time.Second, "a first summary", func() bool { _, _, ok := pc.sink.state(); return ok })
	h := c.Health()
	owner := observe.Owner{Kind: "parity", ID: "rows"}
	for _, p := range parityParts {
		pc.unsub = append(pc.unsub, h.SubscribeRows(p.Task, p.Part, owner))
	}
	return pc
}

func (pc *parityConn) close() {
	for _, u := range pc.unsub {
		u()
	}
	h := pc.conn.Health()
	pc.cancel()
	if h != nil {
		<-h.Done()
	}
}

func waitParity(t *testing.T, limit time.Duration, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(limit); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out: %s", what)
}

// ---- the gate ---------------------------------------------------------------

func TestParityGate(t *testing.T) {
	if os.Getenv("KLYX_PARITY_CONTEXT") == "" {
		t.Skip("set KLYX_PARITY_CONTEXT=kind-klyx-parity, KLYX_PARITY_DIR and KLYX_PARITY_OUT (scripts/lab/klyx-parity.sh up)")
	}
	if os.Getenv("KLYX_PARITY_CONTEXT") != parityContext {
		t.Fatalf("refusing %q: the parity gate runs only against %s", os.Getenv("KLYX_PARITY_CONTEXT"), parityContext)
	}
	l := &parityLab{t: t, dir: os.Getenv("KLYX_PARITY_DIR"), out: os.Getenv("KLYX_PARITY_OUT")}
	if l.dir == "" || l.out == "" {
		t.Fatal("KLYX_PARITY_DIR and KLYX_PARITY_OUT are required")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	l.script = filepath.Join(root, "scripts/lab/klyx-parity.sh")
	l.sh("check") // the exact-target check before anything else (spec §1)
	if err := os.MkdirAll(l.out, 0o700); err != nil {
		t.Fatal(err)
	}
	l.run = parityRun{ID: randomID(t), Commit: gitOut(t, root, "rev-parse", "HEAD"), Dirty: gitOut(t, root, "status", "--porcelain", "--untracked-files=no") != "",
		StartedAt: time.Now().UTC(), Reprobe: parityReprobe.String()}
	_ = os.Remove(filepath.Join(l.out, "manifest.json")) // a stale manifest never passes the frontend stage

	prev := *fleet.CapReprobeEvery
	*fleet.CapReprobeEvery = parityReprobe
	defer func() { *fleet.CapReprobeEvery = prev }()

	none := map[string]string{}
	var cases []string

	// B and cases 1-6: the cluster admin.
	admin := l.connect(filepath.Join(l.dir, "kubeconfig"))
	base := func() caseExpect { return l.oracle("B", nil, none) }
	ex, sum, rows, took := l.settle(admin.sink, base, paritySettle)
	l.capture("B", ex, admin.conn, admin.cc, sum, rows, took)
	cases = append(cases, "B")
	for _, c := range []struct {
		name        string
		controllers map[string]string
	}{
		{"1", none}, {"2", none}, {"3", none}, {"4", none}, {"5a", none}, {"5b", none}, {"5c", none},
		{"6", map[string]string{"certs": "not ready"}},
	} {
		l.sh("case", c.name)
		ex, sum, rows, took := l.settle(admin.sink, func() caseExpect { return l.oracle(c.name, nil, c.controllers) }, paritySettle)
		l.capture(c.name, ex, admin.conn, admin.cc, sum, rows, took)
		cases = append(cases, c.name)
		l.sh("revert", c.name)
		_, _, _, back := l.settle(admin.sink, base, paritySettle)
		l.report = append(l.report, fmt.Sprintf("       baseline back in %v", back.Round(time.Millisecond)))
	}
	admin.close()

	// Case 7: a forbidden kind from the first read (no list, no watch).
	saConfig := filepath.Join(l.dir, "sa-kubeconfig")
	defer os.Remove(saConfig)
	l.sh("rbac", "clusterissuers.cert-manager.io")
	l.sh("sa-kubeconfig")
	sa7 := l.connect(saConfig)
	ex, sum, rows, took = l.settle(sa7.sink, func() caseExpect {
		return l.oracle("7", map[string]string{"clusterissuers": "unreadable:forbidden"}, none)
	}, paritySettle)
	l.capture("7", ex, sa7.conn, sa7.cc, sum, rows, took)
	cases = append(cases, "7")
	sa7.close()

	// Case 8: RBAC revoked after a sync - same connection and incarnation;
	// the open watch ended by an API-server restart (spec §3).
	l.sh("rbac", "none")
	l.sh("sa-kubeconfig")
	sa8 := l.connect(saConfig)
	live, sum, _, _ := l.settle(sa8.sink, func() caseExpect { return l.oracle("8-live", nil, none) }, paritySettle)
	incarnation, liveAt := sum.Incarnation, time.Now()
	l.sh("rbac", "externalsecrets.external-secrets.io")
	l.sh("restart-apiserver")
	restarted := time.Now()
	// the stale part is compared with its last-good set: the oracle read at
	// verified Live, never the current admin objects (spec §1)
	ex8 := l.oracle("8", map[string]string{"externalsecrets": "stale"}, none)
	for i := range ex8.Parts {
		if ex8.Parts[i].Task == "eso" && ex8.Parts[i].Part == "externalsecrets" {
			for _, lp := range live.Parts {
				if lp.Task == "eso" && lp.Part == "externalsecrets" {
					ex8.Parts[i].Roles, ex8.Parts[i].Included, ex8.Parts[i].UIDs = lp.Roles, lp.Included, lp.UIDs
				}
			}
		}
	}
	_, sum, rows, took = l.settle(sa8.sink, func() caseExpect { return ex8 }, parityRestart)
	if sum.Incarnation != incarnation {
		t.Fatalf("case 8: incarnation %d after the revocation, %d before - the same Observer must hold", sum.Incarnation, incarnation)
	}
	// last good is when the authorized stream ended: after verified Live,
	// by the restart's return - and it stays put while re-lists keep failing
	esKind := func(sum observe.Summary) observe.KindCoverage {
		_, p := findPart(sum, "eso", "externalsecrets")
		for _, k := range p.Kinds {
			if k.Kind == "externalsecrets" {
				return k
			}
		}
		t.Fatal("case 8: no externalsecrets kind")
		return observe.KindCoverage{}
	}
	k8 := esKind(sum)
	if k8.LastGoodAt == nil || k8.LastGoodAt.Before(liveAt) || k8.LastGoodAt.After(restarted) {
		t.Fatalf("case 8: last good %v, want between verified Live %v and the restart's return %v", k8.LastGoodAt, liveAt, restarted)
	}
	if k8.Class != "forbidden" && !strings.Contains(strings.ToLower(k8.Error), "forbidden") {
		t.Fatalf("case 8: the recorded fault is %q/%q, want forbidden", k8.Class, k8.Error)
	}
	l.capture("8", ex8, sa8.conn, sa8.cc, sum, rows, took)
	time.Sleep(15 * time.Second)
	later, _, _ := sa8.sink.state()
	if bad := mismatches(ex8, later, nil); len(bad) > 0 && !onlyRowsMissing(bad) {
		t.Fatalf("case 8 did not hold 15s later:\n  %s", strings.Join(bad, "\n  "))
	}
	if k := esKind(later); k.LastGoodAt == nil || !k.LastGoodAt.Equal(*k8.LastGoodAt) || later.Incarnation != incarnation {
		t.Fatalf("case 8: last good moved from %v to %v (incarnation %d)", k8.LastGoodAt, k.LastGoodAt, later.Incarnation)
	}
	l.report = append(l.report, fmt.Sprintf("       last good %s held 15s later (verified Live %s, restart returned %s)",
		k8.LastGoodAt.Format("15:04:05.000"), liveAt.Format("15:04:05.000"), restarted.Format("15:04:05.000")))
	cases = append(cases, "8")
	sa8.close()

	manifest, err := json.MarshalIndent(map[string]any{"run": l.run, "cases": cases}, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(l.out, "manifest.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("\n1c slice 6 parity gate, Go stage: PASS (run %s, commit %s, dirty %v, capability re-probe %v for the run)\n%s",
		l.run.ID, l.run.Commit, l.run.Dirty, parityReprobe, strings.Join(l.report, "\n"))
}

func randomID(t *testing.T) string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func gitOut(t *testing.T, dir string, args ...string) string {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}
