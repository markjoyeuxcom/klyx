package healthsrc

import (
	"context"
	"k8s.io/apimachinery/pkg/types"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/moomora/klyx/internal/certmanager"
	"github.com/moomora/klyx/internal/externalsecrets"
	"github.com/moomora/klyx/internal/karpenter"
	"github.com/moomora/klyx/internal/observe"
)

func cr(apiVersion, kind, ns, name string, spec map[string]any, conds []any, status map[string]any) *unstructured.Unstructured {
	meta := map[string]any{"name": name, "uid": "uid-" + name, "generation": int64(1),
		"annotations":   map[string]any{"kubectl.kubernetes.io/last-applied-configuration": `{"huge":"json"}`, "team": "shop"},
		"managedFields": []any{map[string]any{"manager": "kubectl", "operation": "Apply"}}}
	if ns != "" {
		meta["namespace"] = ns
	}
	if status == nil {
		status = map[string]any{}
	}
	status["conditions"] = conds
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": meta, "spec": spec, "status": status}}
}

func cond(t, status, reason string) map[string]any {
	return map[string]any{"type": t, "status": status, "reason": reason, "observedGeneration": int64(1)}
}

func nativeFixture(now time.Time) []runtime.Object {
	certSpec := func(issuer string) map[string]any {
		return map[string]any{"secretName": "tls", "dnsNames": []any{"shop.example"}, "issuerRef": map[string]any{"name": issuer, "kind": "ClusterIssuer"}}
	}
	return []runtime.Object{
		cr("cert-manager.io/v1", "Certificate", "shop", "web-tls", certSpec("le"), []any{cond("Ready", "True", "Ready")},
			map[string]any{"notAfter": now.Add(30 * 24 * time.Hour).Format(time.RFC3339)}),
		cr("cert-manager.io/v1", "Certificate", "shop", "old-tls", certSpec("le"), []any{cond("Ready", "True", "Ready")},
			map[string]any{"notAfter": now.Add(-time.Hour).Format(time.RFC3339)}),
		cr("cert-manager.io/v1", "Certificate", "shop", "new-tls", certSpec("le"), []any{cond("Issuing", "True", "DoesNotExist")}, nil),
		// A failing Issuer no Certificate uses: it must still light its part.
		cr("cert-manager.io/v1", "Issuer", "shop", "unused", map[string]any{}, []any{cond("Ready", "False", "ErrInitIssuer")}, nil),
		cr("cert-manager.io/v1", "ClusterIssuer", "", "le", map[string]any{}, []any{cond("Ready", "True", "ACMEAccountRegistered")}, nil),
		cr("external-secrets.io/v1beta1", "ExternalSecret", "shop", "db-creds", map[string]any{"refreshInterval": "1h", "secretStoreRef": map[string]any{"name": "vault"}},
			[]any{cond("Ready", "False", "SecretSyncedError")}, nil),
		cr("external-secrets.io/v1beta1", "ExternalSecret", "shop", "api-key", map[string]any{"refreshInterval": "1h", "secretStoreRef": map[string]any{"name": "vault", "kind": "ClusterSecretStore"}},
			[]any{cond("Ready", "True", "SecretSynced")}, map[string]any{"refreshTime": now.Add(-10 * time.Minute).Format(time.RFC3339)}),
		// A failing SecretStore nothing references: it must still light its part.
		cr("external-secrets.io/v1beta1", "SecretStore", "shop", "vault", map[string]any{"provider": map[string]any{"vault": map[string]any{}}},
			[]any{cond("Ready", "False", "InvalidProviderConfig")}, nil),
		cr("external-secrets.io/v1beta1", "ClusterSecretStore", "", "global", map[string]any{"provider": map[string]any{"azurekv": map[string]any{}}},
			[]any{cond("Ready", "True", "Valid")}, nil),
	}
}

func listKinds() map[schema.GroupVersionResource]string {
	return map[schema.GroupVersionResource]string{
		{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}:                 "CertificateList",
		{Group: "cert-manager.io", Version: "v1", Resource: "issuers"}:                      "IssuerList",
		{Group: "cert-manager.io", Version: "v1", Resource: "clusterissuers"}:               "ClusterIssuerList",
		{Group: "external-secrets.io", Version: "v1beta1", Resource: "externalsecrets"}:     "ExternalSecretList",
		{Group: "external-secrets.io", Version: "v1beta1", Resource: "secretstores"}:        "SecretStoreList",
		{Group: "external-secrets.io", Version: "v1beta1", Resource: "clustersecretstores"}: "ClusterSecretStoreList",
	}
}

func task(s *sink, id string) (observe.Task, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.last.Tasks {
		if t.ID == id {
			return t, true
		}
	}
	return observe.Task{}, false
}

func nativePart(s *sink, taskID, partID string) (observe.Part, bool) {
	t, ok := task(s, taskID)
	if !ok {
		return observe.Part{}, false
	}
	for _, p := range t.Parts {
		if p.ID == partID {
			return p, true
		}
	}
	return observe.Part{}, false
}

func TestNativeTasksOnFixtures(t *testing.T) {
	now := time.Now()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds(), nativeFixture(now)...)
	served := func(_ context.Context, group, _, _ string) (observe.Probe, error) {
		switch group {
		case "cert-manager.io":
			return observe.Probe{Served: true, Version: "v1"}, nil
		case "external-secrets.io":
			return observe.Probe{Served: true, Version: "v1beta1"}, nil
		default: // Karpenter is not installed here
			return observe.Probe{Served: false}, nil
		}
	}
	kinds, domains := Native(NativeDeps{Cluster: "c1", Dyn: dyn, Served: served})
	s := &sink{}
	o := observe.New(observe.Config{Cluster: "c1", Sink: s, Kinds: kinds, Domains: domains, Throttle: 20 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); <-o.Done() }()
	o.Start(ctx)

	expect := func(taskID, partID string, total, included int, check func(observe.Part) bool) {
		t.Helper()
		waitFor(t, taskID+"/"+partID, func() bool {
			p, ok := nativePart(s, taskID, partID)
			return ok && p.Coverage == observe.Live && p.Native != nil && p.Native.Total == total && p.Native.Included == included && (check == nil || check(p))
		})
	}
	expect(TaskCerts, "certificates", 3, 2, func(p observe.Part) bool {
		// expired (fault) and issuing (included, progressing role); the
		// healthy one's expiry is the part's next boundary
		return p.Native.Roles.Fault == 1 && p.Native.Roles.Progressing == 1 && p.Native.Roles.OK == 1 &&
			p.Top[0].Name == "old-tls" && p.Top[0].Label == "Expired" && p.Top[0].UID == "uid-old-tls" && p.Next != nil
	})
	expect(TaskCerts, "issuers", 2, 1, func(p observe.Part) bool { return p.Top[0].Name == "unused" && p.Top[0].Role == "fault" })
	expect(TaskESO, "externalsecrets", 2, 1, func(p observe.Part) bool {
		return p.Native.Word == "not synced or refresh overdue" && p.Top[0].Name == "db-creds" && p.Next != nil
	})
	expect(TaskESO, "stores", 2, 1, func(p observe.Part) bool { return p.Top[0].Name == "vault" })
	waitFor(t, "Karpenter not installed: the task is absent", func() bool {
		k, ok := task(s, TaskKarpenter)
		return ok && k.Coverage == observe.Absent
	})
}

// The projection drops managedFields and the last-applied annotation and
// nothing any parser reads.
func TestUnstructuredProjectionParity(t *testing.T) {
	now := time.Now()
	for _, obj := range nativeFixture(now) {
		raw := obj.(*unstructured.Unstructured)
		before := raw.DeepCopy()
		pv, err := ProjectUnstructured(raw)
		if err != nil {
			t.Fatal(err)
		}
		proj := pv.(*unstructured.Unstructured)
		if !reflect.DeepEqual(raw, before) {
			t.Fatal("the projection mutated its input")
		}
		if proj.GetManagedFields() != nil || proj.GetAnnotations()["kubectl.kubernetes.io/last-applied-configuration"] != "" {
			t.Fatal("the projection keeps the bulk it should drop")
		}
		var a, b any
		switch raw.GetKind() {
		case "Certificate":
			a, b = certmanager.ParseCertificate(raw), certmanager.ParseCertificate(proj)
		case "Issuer", "ClusterIssuer":
			a, b = certmanager.ParseIssuer(raw), certmanager.ParseIssuer(proj)
		case "ExternalSecret":
			a, b = externalsecrets.ParseExternalSecret(raw, now), externalsecrets.ParseExternalSecret(proj, now)
		case "SecretStore", "ClusterSecretStore":
			a, b = externalsecrets.ParseStore(raw), externalsecrets.ParseStore(proj)
		}
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("%s %s parses differently after projection:\nraw  %+v\nproj %+v", raw.GetKind(), raw.GetName(), a, b)
		}
	}
	claim := cr("karpenter.sh/v1", "NodeClaim", "", "c1", map[string]any{}, []any{cond("Launched", "True", ""), cond("Registered", "False", "NodeNotFound")}, map[string]any{"nodeName": "n1"})
	pool := cr("karpenter.sh/v1", "NodePool", "", "default", map[string]any{"weight": int64(10), "limits": map[string]any{"cpu": "100"}}, []any{cond("Ready", "True", "")}, nil)
	// S10: Karpenter's kinds project into the structs the rules read, the
	// same as parsing the raw object (a claim is aged at evaluation)
	pc, _ := karpenter.Project(KindNodeClaims)(claim)
	pp, _ := karpenter.Project(KindNodePools)(pool)
	if !reflect.DeepEqual(karpenter.ParseNodeClaim(claim, now), pc.(*karpenter.NodeClaim).At(now)) ||
		!reflect.DeepEqual(karpenter.ParseNodePool(pool), *pp.(*karpenter.NodePool)) {
		t.Fatal("Karpenter objects parse differently after projection")
	}
}

// Top refs carry the version the kind is actually served at: a non-preferred
// served version first, then the version a re-probe moves to - a new kind
// generation whose objects are a different incarnation (Codex 2b-1 review #2).
func TestNativeRefsCarryTheServedVersion(t *testing.T) {
	now := time.Now()
	expired := func(version, uid string) *unstructured.Unstructured {
		u := cr("cert-manager.io/"+version, "Certificate", "shop", "old-tls", map[string]any{"secretName": "tls"},
			[]any{cond("Ready", "True", "Ready")}, map[string]any{"notAfter": now.Add(-time.Hour).Format(time.RFC3339)})
		u.SetUID(types.UID(uid))
		return u
	}
	kinds := map[schema.GroupVersionResource]string{
		{Group: "cert-manager.io", Version: "v1beta2", Resource: "certificates"}: "CertificateList",
		{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}:      "CertificateList",
	}
	for _, v := range []string{"v1beta2", "v1"} { // every cert-manager kind is served at the probed version
		kinds[schema.GroupVersionResource{Group: "cert-manager.io", Version: v, Resource: "issuers"}] = "IssuerList"
		kinds[schema.GroupVersionResource{Group: "cert-manager.io", Version: v, Resource: "clusterissuers"}] = "ClusterIssuerList"
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), kinds, expired("v1beta2", "uid-beta"), expired("v1", "uid-v1"))
	var version atomic.Value
	version.Store("v1beta2")
	served := func(_ context.Context, group, _, _ string) (observe.Probe, error) {
		if group == "cert-manager.io" {
			return observe.Probe{Served: true, Version: version.Load().(string)}, nil
		}
		return observe.Probe{Served: false}, nil
	}
	ks, ds := Native(NativeDeps{Cluster: "c1", Dyn: dyn, Served: served})
	s := &sink{}
	o := observe.New(observe.Config{Cluster: "c1", Sink: s, Kinds: ks, Domains: ds, Throttle: 20 * time.Millisecond, ReprobeEvery: 50 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); <-o.Done() }()
	o.Start(ctx)
	topIs := func(gvr, uid string) func() bool {
		return func() bool {
			p, ok := nativePart(s, TaskCerts, "certificates")
			return ok && len(p.Top) == 1 && p.Top[0].GVR == gvr && p.Top[0].UID == uid
		}
	}
	waitFor(t, "the non-preferred served version", topIs("cert-manager.io/v1beta2/certificates", "uid-beta"))
	version.Store("v1")
	waitFor(t, "the version a re-probe moves to, as a new incarnation", topIs("cert-manager.io/v1/certificates", "uid-v1"))
}
