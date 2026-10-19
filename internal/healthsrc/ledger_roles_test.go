package healthsrc

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"github.com/moomora/klyx/internal/helmcli"
	"github.com/moomora/klyx/internal/observe"
)

// The Line slice 2b §1.1: every ledger part's evaluator emits one role per
// evaluated counted object - keyed by UID (Helm: namespace/name), naming its
// source and served GVR - and stops at Capture.RoleCap.

var fixtureResource = map[string]string{
	"Certificate": KindCertificates, "Issuer": KindIssuers, "ClusterIssuer": KindClusterIssuers,
	"ExternalSecret": KindExternalSecrets, "SecretStore": KindSecretStores, "ClusterSecretStore": KindClusterSecretStores,
	"Kustomization": KindKustomizations, "HelmRelease": KindHelmReleases, "GitRepository": KindGitRepositories,
	"Application": KindApplications, "ApplicationSet": KindApplicationSets,
}

func objectsByKind(t *testing.T, objs []runtime.Object) map[string][]any {
	t.Helper()
	out := map[string][]any{}
	for _, o := range objs {
		u := o.(*unstructured.Unstructured)
		res, ok := fixtureResource[u.GetKind()]
		if !ok {
			t.Fatalf("no resource for %s", u.GetKind())
		}
		pv, err := ProjectUnstructured(u)
		if err != nil {
			t.Fatal(err)
		}
		out[res] = append(out[res], pv)
	}
	return out
}

// checkRoles evaluates every ledger part and checks the role contract.
func checkRoles(t *testing.T, domains []observe.DomainSpec, objs map[string][]any) {
	t.Helper()
	for _, d := range domains {
		for _, p := range d.Parts {
			if !p.Ledger {
				continue
			}
			c := observe.Capture{Now: time.Now(), Objects: objs, RoleCap: 1000}
			res := p.Evaluate(c)
			counted := map[string]int{}
			for _, k := range p.Counted {
				counted[k] = len(objs[k])
			}
			seen := map[string]bool{}
			per := map[string]int{}
			for _, r := range res.Roles {
				if _, ok := counted[r.Source]; !ok || r.Key == "" || seen[r.Key] || r.Role == "" {
					t.Errorf("%s/%s: bad role %+v", d.ID, p.ID, r)
				}
				if r.Source != SnapHelm && r.GVR == "" {
					t.Errorf("%s/%s: role without a served GVR: %+v", d.ID, p.ID, r)
				}
				seen[r.Key] = true
				per[r.Source]++
			}
			for k, n := range counted {
				if per[k] != n {
					t.Errorf("%s/%s: %d roles for %d %s", d.ID, p.ID, per[k], n, k)
				}
			}
			if res.RolesCapped {
				t.Errorf("%s/%s: capped under the cap", d.ID, p.ID)
			}
			// Without a ledger, nothing is built.
			c.RoleCap = 0
			if res := p.Evaluate(c); len(res.Roles) != 0 {
				t.Errorf("%s/%s: roles built with no ledger", d.ID, p.ID)
			}
		}
	}
}

func TestLedgerRolesForNativeAndGitOps(t *testing.T) {
	_, native := Native(NativeDeps{Cluster: "c1"})
	checkRoles(t, native, objectsByKind(t, nativeFixture(time.Now())))
	_, gitops := GitOps(NativeDeps{Cluster: "c1"})
	checkRoles(t, gitops, objectsByKind(t, gitopsFixture()))
}

func TestLedgerRolesForHelmAreKeyedByNamespaceAndName(t *testing.T) {
	_, domains := Helm(HelmDeps{Cluster: "c1"})
	items := HelmItems([]helmcli.Release{{Name: "web", Namespace: "shop", Status: "failed"}, {Name: "db", Namespace: "shop", Status: "deployed"}})
	checkRoles(t, domains, map[string][]any{SnapHelm: items})
	res := domains[0].Parts[0].Evaluate(observe.Capture{Now: time.Now(), Objects: map[string][]any{SnapHelm: items}, RoleCap: 10})
	if res.Roles[0].Key != "shop/web" && res.Roles[1].Key != "shop/web" {
		t.Fatalf("keys: %+v", res.Roles)
	}
}

func TestLedgerRolesStopAtTheCap(t *testing.T) {
	_, native := Native(NativeDeps{Cluster: "c1"})
	objs := objectsByKind(t, nativeFixture(time.Now()))
	for _, d := range native {
		for _, p := range d.Parts {
			if p.ID != "certificates" {
				continue
			}
			res := p.Evaluate(observe.Capture{Now: time.Now(), Objects: objs, RoleCap: 2})
			if len(res.Roles) != 2 || !res.RolesCapped || res.Native.Total != 3 {
				t.Fatalf("cap: %d roles, capped %v, total %d", len(res.Roles), res.RolesCapped, res.Native.Total)
			}
		}
	}
}

// Routes (coverage S7b §5): every route with a parent Klyx reads emits a
// role - a parent absent from a complete, current read is a fault, from any
// other read unknown - partitioned by the route's kind; a route whose only
// parent is a kind Klyx does not read (mesh) emits none.
func TestLedgerRoutesEmitTheirAttachments(t *testing.T) {
	attached := httpRoute("web", []string{"a"}, "web", nil)
	orphan := httpRoute("orphan", []string{"gone"}, "web", nil)
	orphan.SetUID("uid-orphan")
	mesh := httpRoute("mesh", nil, "web", nil)
	mesh.SetUID("uid-mesh")
	mesh.Object["spec"].(map[string]any)["parentRefs"] = []any{map[string]any{"group": "", "kind": "Service", "name": "web"}}
	c := routesCapture(t, []*unstructured.Unstructured{attached, orphan, mesh}, gw("a", "True"))
	res := evalRoutes("c1")(c)
	roles := map[string]string{}
	for _, r := range res.Roles {
		if r.Source != KindHTTPRoutes || r.Key == "" {
			t.Fatalf("route role: %+v", r)
		}
		roles[r.Name] = r.Role + " " + r.Label
	}
	if len(roles) != 2 || roles["web"] != "ok resolved" || roles["orphan"] != "fault parent Gateway not found" {
		t.Fatalf("route roles: %v", roles)
	}
	c.Complete[KindGateways] = false
	for _, r := range evalRoutes("c1")(c).Roles {
		if r.Name == "orphan" && r.Role != "unknown" {
			t.Fatalf("an incomplete read never proves the parent missing: %+v", r)
		}
	}
}

func TestLedgerRolesForPods(t *testing.T) {
	pod := func(name string, phase corev1.PodPhase) any {
		p, err := ProjectPod(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", UID: types.UID("uid-" + name)},
			Status: corev1.PodStatus{Phase: phase}})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	_, domains := Core(CoreDeps{Cluster: "c1"})
	objs := map[string][]any{KindPods: {pod("a", corev1.PodRunning), pod("b", corev1.PodFailed)}}
	for _, d := range domains {
		for _, p := range d.Parts {
			if p.ID != "pods" {
				continue
			}
			res := p.Evaluate(observe.Capture{Now: time.Now(), Objects: objs, RoleCap: 10})
			byKey := map[string]observe.RoleEntry{}
			for _, r := range res.Roles {
				byKey[r.Key] = r
			}
			b := byKey["uid-b"]
			if len(res.Roles) != 2 || b.Source != KindPods || b.GVR != "v1/pods" || b.Role != "fault" || b.Reason == "" || byKey["uid-a"].Reason != "" {
				t.Fatalf("pod roles: %+v", res.Roles)
			}
		}
	}
}
