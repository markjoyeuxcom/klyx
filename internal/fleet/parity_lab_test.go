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
//	  go test ./internal/fleet -run '^TestParityGate$' -v -count=1 -timeout 60m
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
	active string // the case applied and not yet reverted (cleanup reverts it)
	rbac   bool   // the restricted identity may hold a denial (cleanup restores it)
}

func (l *parityLab) shErr(args ...string) (string, error) {
	cmd := exec.Command("bash", append([]string{l.script}, args...)...)
	cmd.Env = append(os.Environ(), "KLYX_PARITY_DIR="+l.dir)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (l *parityLab) sh(args ...string) string {
	l.t.Helper()
	out, err := l.shErr(args...)
	if err != nil {
		l.t.Fatalf("klyx-parity.sh %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func (l *parityLab) apply(name string) {
	l.t.Helper()
	l.active = name
	l.sh("case", name)
}

func (l *parityLab) revert(name string) {
	l.t.Helper()
	l.sh("revert", name)
	l.active = ""
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

// ---- the sink: the latest summary, every part's rows and their DTOs -----

// dtoRecorder is the frontend's side of the wire: the production
// HealthSink's emitted rows messages (the converted lens DTOs).
type dtoRecorder struct {
	mu   sync.Mutex
	last map[string]json.RawMessage
}

func (d *dtoRecorder) Emit(name string, data any) {
	b, err := json.Marshal(data)
	if err != nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.last == nil {
		d.last = map[string]json.RawMessage{}
	}
	d.last[name] = b
}

func (d *dtoRecorder) get(name string) json.RawMessage {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.last[name]
}

type paritySink struct {
	mu   sync.Mutex
	sum  observe.Summary
	ok   bool
	rows map[string]observe.Rows
	dtos map[string]json.RawMessage
	rec  *dtoRecorder
	hs   *appbridge.HealthSink
}

func newParitySink() *paritySink {
	rec := &dtoRecorder{}
	return &paritySink{rec: rec, hs: appbridge.NewHealthSink(rec)}
}

func (s *paritySink) Summary(x observe.Summary) { s.mu.Lock(); s.sum, s.ok = x, true; s.mu.Unlock() }
func (s *paritySink) Rows(r observe.Rows) {
	// the production sink converts the same capture the gate reads, so the
	// DTO and the rows are paired by construction
	s.hs.Rows(r)
	dto := s.rec.get(appbridge.HealthRowsEventPrefix + r.Cluster + ":" + r.Task + "/" + r.Part.ID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rows == nil {
		s.rows, s.dtos = map[string]observe.Rows{}, map[string]json.RawMessage{}
	}
	key := r.Task + "/" + r.Part.ID
	s.rows[key] = r
	if dto != nil {
		s.dtos[key] = dto
	} else {
		delete(s.dtos, key)
	}
}
func (s *paritySink) state() (observe.Summary, map[string]observe.Rows, map[string]json.RawMessage, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := make(map[string]observe.Rows, len(s.rows))
	for k, v := range s.rows {
		rows[k] = v
	}
	dtos := make(map[string]json.RawMessage, len(s.dtos))
	for k, v := range s.dtos {
		dtos[k] = v
	}
	return s.sum, rows, dtos, s.ok
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
	{"flux", "fluxinstance", "native", []string{"fluxinstances"}},
	{"flux", "resourcesets", "native", []string{"resourcesets", "resourcesetinputproviders"}},
	{"flux", "images", "native", []string{"imagerepositories", "imagepolicies", "imageupdateautomations"}},
	{"flux", "composition", "native", []string{"artifactgenerators", "externalartifacts"}},
	{"flux", "receivers", "native", []string{"receivers"}},
	{"argo", "applications", "native", []string{"applications"}},
	{"argo", "applicationsets", "native", []string{"applicationsets"}},
	{"certs", "certificates", "native", []string{"certificates"}},
	{"certs", "issuers", "native", []string{"issuers", "clusterissuers"}},
	{"eso", "externalsecrets", "native", []string{"externalsecrets"}},
	{"eso", "stores", "native", []string{"secretstores", "clustersecretstores"}},
	{"gateway", "gateways", "native", []string{"gateways"}},
	{"gateway", "listenersets", "native", []string{"listenersets"}},                                              // coverage S7a
	{"gateway", "routes", "native", []string{"httproutes", "grpcroutes", "tlsroutes", "tcproutes", "udproutes"}}, // coverage S7b
	{"karpenter", "nodeclaims", "native", []string{"nodeclaims"}},
	{"karpenter", "nodepools", "native", []string{"nodepools"}},
	// coverage S10: the NodeClasses are evidence (their pools carry the verdict)
	{"karpenter", "nodeclasses", "evidence", []string{"ec2nodeclasses", "aksnodeclasses", "nodeclasses"}},
	{"velero", "schedules", "native", []string{"schedules"}},
	{"velero", "locations", "native", []string{"backupstoragelocations"}},
	{"velero", "backups", "native", []string{"backups"}},
	{"admission", "webhooks", "native", []string{"validatingwebhookconfigurations", "mutatingwebhookconfigurations"}},
	{"admission", "policies", "native", []string{"validatingadmissionpolicies", "mutatingadmissionpolicies"}},
	// coverage S4: only a LoadBalancer Service is a member (parityMemberOf)
	{"lb", "services", "native", []string{"services"}},
	{"lb", "pools", "native", []string{"ipaddresspools", "ciliumloadbalancerippools"}},
	{"lb", "metallb", "native", []string{"configurationstates"}},
	// coverage S8a: the fixture's real Alertmanager, apart from every object
	{"alerts", "alerts", "alerts", nil},
	// coverage S9: standalone HPAs only (a KEDA-owned HPA is its
	// ScaledObject's fact row, parityMemberOf), KEDA's scalers, and its
	// authentications (evidence)
	{"autoscaling", "hpas", "native", []string{"horizontalpodautoscalers"}},
	{"keda", "scaledobjects", "native", []string{"scaledobjects"}},
	{"keda", "scaledjobs", "native", []string{"scaledjobs"}},
	{"keda", "authentications", "evidence", []string{"triggerauthentications", "clustertriggerauthentications"}},
}

// parityAlertsCounted is the fixture's posted alerts that count: two
// critical and one warning, active (alerts_objects in parity/fixture.sh).
const parityAlertsCounted = 3

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
	"listenersets": "listenersets.gateway.networking.k8s.io",
	"grpcroutes":   "grpcroutes.gateway.networking.k8s.io", "tlsroutes": "tlsroutes.gateway.networking.k8s.io",
	"tcproutes": "tcproutes.gateway.networking.k8s.io", "udproutes": "udproutes.gateway.networking.k8s.io",
	"nodeclaims": "nodeclaims.karpenter.sh", "nodepools": "nodepools.karpenter.sh",
	"ec2nodeclasses": "ec2nodeclasses.karpenter.k8s.aws", "aksnodeclasses": "aksnodeclasses.karpenter.azure.com",
	"nodeclasses":   "nodeclasses.eks.amazonaws.com",
	"fluxinstances": "fluxinstances.fluxcd.controlplane.io", "resourcesets": "resourcesets.fluxcd.controlplane.io",
	"resourcesetinputproviders": "resourcesetinputproviders.fluxcd.controlplane.io",
	"imagerepositories":         "imagerepositories.image.toolkit.fluxcd.io", "imagepolicies": "imagepolicies.image.toolkit.fluxcd.io",
	"imageupdateautomations": "imageupdateautomations.image.toolkit.fluxcd.io",
	"artifactgenerators":     "artifactgenerators.source.extensions.fluxcd.io", "externalartifacts": "externalartifacts.source.toolkit.fluxcd.io",
	"receivers": "receivers.notification.toolkit.fluxcd.io",
	"schedules": "schedules.velero.io", "backupstoragelocations": "backupstoragelocations.velero.io", "backups": "backups.velero.io",
	"validatingwebhookconfigurations": "validatingwebhookconfigurations.admissionregistration.k8s.io",
	"mutatingwebhookconfigurations":   "mutatingwebhookconfigurations.admissionregistration.k8s.io",
	"validatingadmissionpolicies":     "validatingadmissionpolicies.admissionregistration.k8s.io",
	"mutatingadmissionpolicies":       "mutatingadmissionpolicies.admissionregistration.k8s.io",
	"services":                        "services", "ipaddresspools": "ipaddresspools.metallb.io",
	"ciliumloadbalancerippools": "ciliumloadbalancerippools.cilium.io", "configurationstates": "configurationstates.metallb.io",
	"horizontalpodautoscalers": "horizontalpodautoscalers.autoscaling",
	"scaledobjects":            "scaledobjects.keda.sh", "scaledjobs": "scaledjobs.keda.sh",
	"triggerauthentications": "triggerauthentications.keda.sh", "clustertriggerauthentications": "clustertriggerauthentications.keda.sh",
}

// parityMember is one counted object's declared evaluation.
type parityMember struct {
	Role     string `json:"role"`
	Included bool   `json:"included"`
}

// partExpect is the oracle for one part at one case.
type partExpect struct {
	Task       string                  `json:"task"`
	Part       string                  `json:"part"`
	Classifier string                  `json:"classifier"`
	Counted    []string                `json:"counted"`
	Roles      observe.Counts          `json:"roles"` // Total included
	Included   int                     `json:"included"`
	Members    map[string]parityMember `json:"members"`  // uid -> declared evaluation
	Coverage   map[string]string       `json:"coverage"` // counted kind -> live | stale | unreadable:<class>
	// Alerts is an alerts part's declared counted alerts (coverage S8a): the
	// fixture's posted active critical and warning alerts.
	Alerts int `json:"alerts,omitempty"`
}

// caseExpect is one case's declared state.
type caseExpect struct {
	Case        string            `json:"case"`
	Parts       []partExpect      `json:"parts"`
	Controllers map[string]string `json:"controllers"` // task -> "" | not ready | unknown | managed
	State       string            `json:"state"`       // the connection: Synced | Degraded (exact)
}

type parityObj struct {
	Metadata struct {
		UID             string            `json:"uid"`
		Annotations     map[string]string `json:"annotations"`
		OwnerReferences []struct {
			APIVersion string `json:"apiVersion"`
			Kind       string `json:"kind"`
			Controller *bool  `json:"controller"`
		} `json:"ownerReferences"`
	} `json:"metadata"`
	Spec struct {
		Type string `json:"type"`
	} `json:"spec"`
}

// parityMemberOf: whether a counted kind's object is a member of the part
// (coverage S4: the lb services part counts only LoadBalancer Services;
// coverage S9: the hpas part counts only HPAs no ScaledObject controls).
func parityMemberOf(task, part string, o parityObj) bool {
	switch {
	case task == "lb" && part == "services":
		return o.Spec.Type == "LoadBalancer"
	case task == "autoscaling" && part == "hpas":
		for _, r := range o.Metadata.OwnerReferences {
			if r.Controller != nil && *r.Controller && r.Kind == "ScaledObject" && strings.HasPrefix(r.APIVersion, "keda.sh/") {
				return false
			}
		}
	}
	return true
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

// oracle builds a case's expectations from the objects' declarations;
// coverage overrides a kind's expected coverage.
func (l *parityLab) oracle(name, state string, coverage map[string]string, controllers map[string]string) caseExpect {
	ex := caseExpect{Case: name, Controllers: map[string]string{}, State: state}
	for _, t := range []string{"flux", "argo", "certs", "eso", "gateway", "karpenter", "velero", "lb", "keda"} {
		ex.Controllers[t] = controllers[t]
	}
	for _, p := range parityParts {
		pe := partExpect{Task: p.Task, Part: p.Part, Classifier: p.Classifier, Counted: p.Counted, Coverage: map[string]string{}, Members: map[string]parityMember{}}
		if p.Classifier == "alerts" {
			pe.Alerts = parityAlertsCounted
		}
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
				if !parityMemberOf(p.Task, p.Part, o) {
					continue
				}
				m := parityMember{Role: o.Metadata.Annotations["klyx.dev/parity-role"], Included: o.Metadata.Annotations["klyx.dev/parity-included"] == "true"}
				if m.Role == "" {
					m.Role = "ok"
				}
				addRole(&pe.Roles, m.Role)
				if m.Included {
					pe.Included++
				}
				pe.Members[o.Metadata.UID] = m
			}
		}
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

// kindRole names health.Kind's values (verdict.go: Fault=0 ... OK=5).
var kindRole = []string{"fault", "degraded", "unknown", "progressing", "paused", "ok"}

// rowEvals derives each row's own evaluation from the row body, from the
// part's declared source - a verdict row's Health, a route's reduced
// attachment, or a native rule's Eval - never from the part's copied
// counts (review finding 2). A row whose source carries no evaluation is
// an error, never a zero value read as a role.
func rowEvals(r observe.Rows, key string) (map[string]parityMember, error) {
	b, err := json.Marshal(r.Rows)
	if err != nil {
		return nil, err
	}
	var env struct {
		Rows []struct {
			Ref  struct{ UID string }
			Eval struct {
				Included bool
				Role     int
				Label    string
			}
			Data struct {
				Health   *struct{ Kind int }
				Role     string
				Included bool
				// a listed fact's markers (coverage S2b, S3a): an uncounted
				// Backup, a binding
				Counted *bool
				Binding json.RawMessage
				// a KEDA-owned HPA (coverage S9): its ScaledObject's fact
				KEDAOwner string
			}
		}
	}
	if err := json.Unmarshal(b, &env); err != nil {
		return nil, err
	}
	out := make(map[string]parityMember, len(env.Rows))
	for _, x := range env.Rows {
		// a listed fact, by its part's own marker: no evaluation, and no member
		// of the counted set (FluxInstance rows carry an unrelated Binding)
		if (key == "velero/backups" && x.Data.Counted != nil && !*x.Data.Counted) ||
			(key == "admission/policies" && len(x.Data.Binding) > 0 && string(x.Data.Binding) != "null") ||
			(key == "autoscaling/hpas" && x.Data.KEDAOwner != "") {
			continue
		}
		var m parityMember
		switch {
		case key == "workloads/workloads" || key == "pods/pods" || key == "nodes/nodes":
			if x.Data.Health == nil || x.Data.Health.Kind < 0 || x.Data.Health.Kind >= len(kindRole) {
				return nil, fmt.Errorf("row %s carries no verdict", x.Ref.UID)
			}
			m.Role = kindRole[x.Data.Health.Kind]
		case key == "gateway/routes":
			if x.Data.Role == "" {
				return nil, fmt.Errorf("route %s carries no reduced evaluation", x.Ref.UID)
			}
			m = parityMember{Role: x.Data.Role, Included: x.Data.Included}
		default:
			if x.Eval.Label == "" || x.Eval.Role < 0 || x.Eval.Role >= len(kindRole) {
				return nil, fmt.Errorf("row %s carries no rule evaluation", x.Ref.UID)
			}
			m = parityMember{Role: kindRole[x.Eval.Role], Included: x.Eval.Included}
		}
		out[x.Ref.UID] = m
	}
	return out, nil
}

// memberDiff compares derived row evaluations with the declared members.
func memberDiff(got, want map[string]parityMember, native bool) []string {
	var bad []string
	for uid, w := range want {
		g, ok := got[uid]
		switch {
		case !ok:
			bad = append(bad, "missing "+uid)
		case g.Role != w.Role || (native && g.Included != w.Included):
			bad = append(bad, fmt.Sprintf("%s %+v, declared %+v", uid, g, w))
		}
	}
	for uid := range got {
		if _, ok := want[uid]; !ok {
			bad = append(bad, "unexpected "+uid)
		}
	}
	sort.Strings(bad)
	return bad
}

type connState struct{ State, Reason string }

// mismatches compares one summary (and the subscribed rows) with a case's
// oracle: every expected part, its coverage, its counts, every controller,
// the exact connection - and every row's own evaluation at the same
// incarnation and Ver.
func mismatches(ex caseExpect, sum observe.Summary, rows map[string]observe.Rows, conn connState) []string {
	var bad []string
	if conn.State != ex.State {
		bad = append(bad, fmt.Sprintf("connection state %s, want %s", conn.State, ex.State))
	}
	switch ex.State {
	case "Synced":
		if sum.Connection != "connected" {
			bad = append(bad, fmt.Sprintf("summary connection %q, want \"connected\"", sum.Connection))
		}
	case "Degraded":
		if conn.Reason == "" || sum.Connection != "connected · "+conn.Reason {
			bad = append(bad, fmt.Sprintf("summary connection %q, want \"connected · \" and the reason %q", sum.Connection, conn.Reason))
		}
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
		if pe.Classifier == "alerts" {
			// coverage S8a: counted apart, never roles; checked when the
			// alerts read is live
			switch {
			case p.Alerts == nil:
				bad = append(bad, key+" has no alert counts")
			case p.Coverage == observe.Live && p.Alerts.Counted != pe.Alerts:
				bad = append(bad, fmt.Sprintf("%s counted %d alerts, want %d", key, p.Alerts.Counted, pe.Alerts))
			case p.Coverage != observe.Live:
				bad = append(bad, fmt.Sprintf("%s coverage %q, want live", key, p.Coverage))
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
		if rows == nil {
			continue // a re-check of the summary alone
		}
		r, ok := rows[key]
		if !ok {
			bad = append(bad, key+" has no rows")
			continue
		}
		if r.Incarnation != sum.Incarnation || r.Part.Ver != p.Ver {
			bad = append(bad, fmt.Sprintf("%s rows at incarnation %d ver %d, summary %d ver %d", key, r.Incarnation, r.Part.Ver, sum.Incarnation, p.Ver))
			continue
		}
		evals, err := rowEvals(r, key)
		if err != nil {
			bad = append(bad, fmt.Sprintf("%s rows: %v", key, err))
			continue
		}
		if d := memberDiff(evals, pe.Members, pe.Classifier == "native"); len(d) > 0 {
			bad = append(bad, fmt.Sprintf("%s row evaluations: %s", key, strings.Join(d, "; ")))
		}
		var derived observe.Counts
		derivedInc := 0
		for _, m := range evals {
			addRole(&derived, m.Role)
			if m.Included {
				derivedInc++
			}
		}
		if derived != got || (inc >= 0 && derivedInc != inc) {
			bad = append(bad, fmt.Sprintf("%s rows derive %+v/%d, summary %+v/%d", key, derived, derivedInc, got, inc))
		}
	}
	return bad
}

// settle waits for the barrier: every mismatch gone in one summary. The
// oracle is re-read every 2s, so both sides describe the same cluster
// state (a Recreate rollout's old pod is gone from both, never one).
func (l *parityLab) settle(pc *parityConn, oracle func() caseExpect, limit time.Duration) settled {
	l.t.Helper()
	t0 := time.Now()
	ex, read := oracle(), time.Now()
	var last []string
	for end := t0.Add(limit); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		if time.Since(read) > 2*time.Second {
			ex, read = oracle(), time.Now()
		}
		sum, rows, dtos, ok := pc.sink.state()
		if !ok {
			continue
		}
		snap := pc.conn.Snapshot()
		if last = mismatches(ex, sum, rows, connState{snap.State.String(), snap.Reason}); len(last) == 0 {
			return settled{ex: ex, sum: sum, rows: rows, dtos: dtos, took: time.Since(t0)}
		}
	}
	l.t.Fatalf("case %s did not settle within %v:\n  %s", ex.Case, limit, strings.Join(last, "\n  "))
	return settled{}
}

type settled struct {
	ex   caseExpect
	sum  observe.Summary
	rows map[string]observe.Rows
	dtos map[string]json.RawMessage
	took time.Duration
}

// ---- capture (spec §3) ---------------------------------------------------

type parityCapture struct {
	Run     parityRun                  `json:"run"`
	Case    string                     `json:"case"`
	Expect  caseExpect                 `json:"expect"`
	Summary observe.Summary            `json:"summary"`
	Cluster appbridge.ClusterDTO       `json:"cluster"`
	Rows    []observe.Rows             `json:"rows"`
	DTOs    map[string]json.RawMessage `json:"dtos"` // task/part -> the emitted rows message (the lens DTOs)
	Settled string                     `json:"settled"`
}

func (l *parityLab) capture(name string, pc *parityConn, s settled) {
	l.t.Helper()
	cluster := appbridge.ToDTO(pc.conn.Snapshot(), pc.cc, time.Now())
	if cluster.State != s.ex.State {
		l.t.Fatalf("case %s: the ClusterDTO state is %s at capture, want %s", name, cluster.State, s.ex.State)
	}
	c := parityCapture{Run: l.run, Case: name, Expect: s.ex, Summary: s.sum, Cluster: cluster, DTOs: s.dtos, Settled: s.took.Round(time.Millisecond).String()}
	keys := make([]string, 0, len(s.rows))
	for k := range s.rows {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		c.Rows = append(c.Rows, s.rows[k])
	}
	b, err := json.MarshalIndent(c, "", " ")
	if err != nil {
		l.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(l.out, name+".json"), b, 0o600); err != nil {
		l.t.Fatal(err)
	}
	l.report = append(l.report, fmt.Sprintf("  %-4s settled in %v (incarnation %d, seq %d, %s)", name, s.took.Round(time.Millisecond), s.sum.Incarnation, s.sum.Seq, cluster.State))
}

// ---- connections ----------------------------------------------------------

type parityConn struct {
	conn   *fleet.ClusterConn
	sink   *paritySink
	cc     config.ClusterConfig
	cancel context.CancelFunc
	unsub  []func()
	once   sync.Once
}

func (l *parityLab) connect(kubeconfig string) *parityConn {
	l.t.Helper()
	cc := config.ClusterConfig{Name: parityContext, Context: parityContext, Kubeconfig: kubeconfig,
		// coverage S8a: the fixture's Alertmanager, this cluster's alerts only
		Alertmanager: &config.EndpointConfig{ServiceRef: &config.MetricsServiceRef{Namespace: "parity-alerting", Name: "alertmanager", Port: "web"},
			Match: map[string]string{"cluster": "kind-klyx-parity"}}}
	conn, err := fleet.DefaultConnFactory(clock.Real{}, fleet.Credentials{})(cc)
	if err != nil {
		l.t.Fatal(err)
	}
	c := conn.(*fleet.ClusterConn)
	pc := &parityConn{conn: c, sink: newParitySink(), cc: cc}
	c.SetHealthSink(pc.sink)
	ctx, cancel := context.WithCancel(context.Background())
	pc.cancel = cancel
	c.Start(ctx)
	l.t.Cleanup(pc.close) // a failed assertion never leaves a connection open
	waitParity(l.t, 120*time.Second, "a first summary", func() bool { _, _, _, ok := pc.sink.state(); return ok })
	h := c.Health()
	owner := observe.Owner{Kind: "parity", ID: "rows"}
	for _, p := range parityParts {
		pc.unsub = append(pc.unsub, h.SubscribeRows(p.Task, p.Part, owner))
	}
	return pc
}

func (pc *parityConn) close() {
	pc.once.Do(func() {
		for _, u := range pc.unsub {
			u()
		}
		h := pc.conn.Health()
		pc.cancel()
		if h != nil {
			<-h.Done()
		}
	})
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
	saConfig := filepath.Join(l.dir, "sa-kubeconfig")
	// Registered first, so it runs after every connection has closed: the
	// fixture back to its baseline whatever failed. A failed restoration is
	// reported, never hidden (review finding 4).
	t.Cleanup(func() {
		*fleet.CapReprobeEvery = prev
		if l.active != "" {
			if out, err := l.shErr("revert", l.active); err != nil {
				t.Errorf("cleanup: revert %s: %v\n%s", l.active, err, out)
			}
		}
		if l.rbac {
			if out, err := l.shErr("rbac", "none"); err != nil {
				t.Errorf("cleanup: restore the observer identity's grants: %v\n%s", err, out)
			}
		}
		if err := os.Remove(saConfig); err != nil && !os.IsNotExist(err) {
			t.Errorf("cleanup: remove the token kubeconfig: %v", err)
		}
	})

	// coverage S10: an attributed AWS and Azure stand-in and EKS Auto Mode's
	// node label: Karpenter's controller task is "managed" in every case (a
	// note, never attention or a gap)
	none := map[string]string{"karpenter": "managed"}
	with := func(task, state string) map[string]string {
		return map[string]string{"karpenter": "managed", task: state}
	}
	var cases []string

	// B and cases 1-6b: the cluster admin.
	admin := l.connect(filepath.Join(l.dir, "kubeconfig"))
	base := func() caseExpect { return l.oracle("B", "Synced", nil, none) }
	l.capture("B", admin, l.settle(admin, base, paritySettle))
	cases = append(cases, "B")
	for _, c := range []struct {
		name, state string
		controllers map[string]string
	}{
		{"1", "Synced", none}, {"2", "Synced", none}, {"3", "Synced", none}, {"4", "Synced", none},
		{"5a", "Synced", none}, {"5b", "Synced", none}, {"5c", "Synced", none}, {"5d", "Synced", none}, {"5e", "Synced", none},
		{"6", "Synced", with("certs", "not ready")},   // cert-manager is not a connection subsystem
		{"6b", "Degraded", with("flux", "not ready")}, // the GitOps tier degrades the connection
		{"9", "Synced", none},                         // coverage S9: a fallback ScaledObject and an HPA that cannot scale
		{"10", "Synced", none},                        // coverage S10: a failing EC2NodeClass lights its two pools once each
	} {
		l.apply(c.name)
		l.capture(c.name, admin, l.settle(admin, func() caseExpect { return l.oracle(c.name, c.state, nil, c.controllers) }, paritySettle))
		cases = append(cases, c.name)
		l.revert(c.name)
		back := l.settle(admin, base, paritySettle) // Synced again, exactly
		l.report = append(l.report, fmt.Sprintf("       baseline back in %v", back.took.Round(time.Millisecond)))
	}
	admin.close()

	// Case 7: a forbidden kind from the first read (no list, no watch); then
	// the grant restored and the baseline recovered on the same connection.
	l.rbac = true
	l.sh("rbac", "clusterissuers.cert-manager.io")
	l.sh("sa-kubeconfig")
	sa7 := l.connect(saConfig)
	l.capture("7", sa7, l.settle(sa7, func() caseExpect {
		return l.oracle("7", "Synced", map[string]string{"clusterissuers": "unreadable:forbidden"}, none)
	}, paritySettle))
	cases = append(cases, "7")
	inc7, _, _, _ := sa7.sink.state()
	l.sh("rbac", "none")
	rec7 := l.settle(sa7, base, paritySettle)
	if rec7.sum.Incarnation != inc7.Incarnation {
		t.Fatalf("case 7 recovered on incarnation %d, not %d", rec7.sum.Incarnation, inc7.Incarnation)
	}
	l.report = append(l.report, fmt.Sprintf("       grant restored: baseline back on the same connection in %v", rec7.took.Round(time.Millisecond)))
	sa7.close()

	// Case 8: RBAC revoked after a sync - same connection and incarnation;
	// the open watch ended by an API-server restart (spec §3); then the
	// grant restored and the baseline recovered, still the same Observer.
	l.sh("sa-kubeconfig")
	sa8 := l.connect(saConfig)
	live := l.settle(sa8, func() caseExpect { return l.oracle("8-live", "Synced", nil, none) }, paritySettle)
	incarnation, liveAt := live.sum.Incarnation, time.Now()
	l.sh("rbac", "externalsecrets.external-secrets.io")
	l.sh("restart-apiserver")
	restarted := time.Now()
	// the stale part is compared with its last-good set: the oracle read at
	// verified Live, never the current admin objects (spec §1)
	ex8 := l.oracle("8", "Synced", map[string]string{"externalsecrets": "stale"}, none)
	for i := range ex8.Parts {
		if ex8.Parts[i].Task == "eso" && ex8.Parts[i].Part == "externalsecrets" {
			for _, lp := range live.ex.Parts {
				if lp.Task == "eso" && lp.Part == "externalsecrets" {
					ex8.Parts[i].Roles, ex8.Parts[i].Included, ex8.Parts[i].Members = lp.Roles, lp.Included, lp.Members
				}
			}
		}
	}
	s8 := l.settle(sa8, func() caseExpect { return ex8 }, parityRestart)
	if s8.sum.Incarnation != incarnation {
		t.Fatalf("case 8: incarnation %d after the revocation, %d before - the same Observer must hold", s8.sum.Incarnation, incarnation)
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
	k8 := esKind(s8.sum)
	if k8.LastGoodAt == nil || k8.LastGoodAt.Before(liveAt) || k8.LastGoodAt.After(restarted) {
		t.Fatalf("case 8: last good %v, want between verified Live %v and the restart's return %v", k8.LastGoodAt, liveAt, restarted)
	}
	if k8.Class != "forbidden" && !strings.Contains(strings.ToLower(k8.Error), "forbidden") {
		t.Fatalf("case 8: the recorded fault is %q/%q, want forbidden", k8.Class, k8.Error)
	}
	l.capture("8", sa8, s8)
	cases = append(cases, "8")
	time.Sleep(15 * time.Second)
	later, _, _, _ := sa8.sink.state()
	snap := sa8.conn.Snapshot()
	if bad := mismatches(ex8, later, nil, connState{snap.State.String(), snap.Reason}); len(bad) > 0 {
		t.Fatalf("case 8 did not hold 15s later:\n  %s", strings.Join(bad, "\n  "))
	}
	if k := esKind(later); k.LastGoodAt == nil || !k.LastGoodAt.Equal(*k8.LastGoodAt) || later.Incarnation != incarnation {
		t.Fatalf("case 8: last good moved from %v to %v (incarnation %d)", k8.LastGoodAt, k.LastGoodAt, later.Incarnation)
	}
	l.report = append(l.report, fmt.Sprintf("       last good %s held 15s later (verified Live %s, restart returned %s)",
		k8.LastGoodAt.Format("15:04:05.000"), liveAt.Format("15:04:05.000"), restarted.Format("15:04:05.000")))
	l.sh("rbac", "none")
	rec8 := l.settle(sa8, base, paritySettle)
	if rec8.sum.Incarnation != incarnation {
		t.Fatalf("case 8 recovered on incarnation %d, not %d", rec8.sum.Incarnation, incarnation)
	}
	l.rbac = false
	l.report = append(l.report, fmt.Sprintf("       grant restored: baseline back on the same Observer in %v", rec8.took.Round(time.Millisecond)))
	sa8.close()

	// the manifest only after every case and every recovery passed
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
