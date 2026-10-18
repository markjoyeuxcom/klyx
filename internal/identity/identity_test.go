package identity

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/capability"
)

const (
	roleA  = "arn:aws:iam::123456789012:role/app-a"
	roleB  = "arn:aws:iam::123456789012:role/app-b"
	guidA  = "11111111-2222-3333-4444-555555555555"
	guidB  = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	tenant = "99999999-8888-7777-6666-555555555555"
)

var t0 = time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)

func sa(ann map[string]string) Declared {
	return DeclaredOf(&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "web", UID: "sa-uid", Annotations: ann}})
}

func tokenVol(name, path, aud string) corev1.Volume {
	return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{
		{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Path: path, Audience: aud}}}}}}
}

type podOpt func(*corev1.Pod)

func pod(env []corev1.EnvVar, mounts []corev1.VolumeMount, vols []corev1.Volume, opts ...podOpt) Pod {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "web-1", UID: "p1", CreationTimestamp: metav1.NewTime(t0)},
		Spec: corev1.PodSpec{ServiceAccountName: "web", NodeName: "n1", Volumes: vols,
			Containers: []corev1.Container{{Name: "app", Env: env, VolumeMounts: mounts}}}}
	for _, o := range opts {
		o(p)
	}
	return PodOf(p)
}

func env(kv ...string) []corev1.EnvVar {
	var out []corev1.EnvVar
	for i := 0; i < len(kv); i += 2 {
		out = append(out, corev1.EnvVar{Name: kv[i], Value: kv[i+1]})
	}
	return out
}

const irsaDir = "/var/run/secrets/eks.amazonaws.com/serviceaccount"

func irsaPod(role string, opts ...podOpt) Pod {
	return pod(env(EnvRoleARN, role, EnvWebTokenFile, irsaDir+"/token"),
		[]corev1.VolumeMount{{Name: "aws-iam-token", MountPath: irsaDir, ReadOnly: true}},
		[]corev1.Volume{tokenVol("aws-iam-token", "token", AudienceSTS)}, opts...)
}

func only(t *testing.T, rs []Reading, adapter string) Reading {
	t.Helper()
	for _, r := range rs {
		if r.Adapter == adapter {
			return r
		}
	}
	t.Fatalf("no %s reading in %+v", adapter, rs)
	return Reading{}
}

func TestIRSAComparison(t *testing.T) {
	if r := only(t, Compare(sa(map[string]string{AnnRoleARN: roleA}), irsaPod(roleA), PlatformOther), AdapterAWS); r.State != StateMatch || r.Compared != "role and audience" {
		t.Fatalf("match: %+v", r)
	}
	if r := only(t, Compare(sa(map[string]string{AnnRoleARN: roleB}), irsaPod(roleA), PlatformOther), AdapterAWS); r.State != StateDiffers || !strings.Contains(r.Text, roleA) {
		t.Fatalf("a stale role: %+v", r)
	}
	if r := only(t, Compare(sa(map[string]string{AnnRoleARN: roleA, AnnAudience: "custom"}), irsaPod(roleA), PlatformOther), AdapterAWS); r.State != StateDiffers || !strings.Contains(r.Text, "audience") {
		t.Fatalf("an audience mismatch: %+v", r)
	}
	if r := only(t, Compare(sa(nil), irsaPod(roleA), PlatformOther), AdapterAWS); r.State != StateUndeclared {
		t.Fatalf("undeclared: %+v", r)
	}
	none := pod(nil, nil, nil)
	if r := only(t, Compare(sa(map[string]string{AnnRoleARN: roleA}), none, PlatformOther), AdapterAWS); r.State != StateNotConfig || !strings.Contains(r.Text, "2026-10-18T09:00:00Z") {
		t.Fatalf("created before the annotation: %+v", r)
	}
	if rs := Compare(sa(nil), none, PlatformOther); len(rs) != 0 {
		t.Fatalf("nothing declared, nothing given: %+v", rs)
	}
	skipped := pod(nil, nil, nil, func(p *corev1.Pod) { p.Annotations = map[string]string{AnnAWSSkip: "sidecar, app"} })
	if r := only(t, Compare(sa(map[string]string{AnnRoleARN: roleA}), skipped, PlatformOther), AdapterAWS); r.State != StateExcluded {
		t.Fatalf("skipped (comma): %+v", r)
	}
	optOut := pod(nil, nil, nil, func(p *corev1.Pod) { p.Labels = map[string]string{LabelAWSOptOut: ""} })
	if r := only(t, Compare(sa(map[string]string{AnnRoleARN: roleA}), optOut, PlatformOther), AdapterAWS); r.State != StateExcluded {
		t.Fatalf("opt-out label: %+v", r)
	}
	withEnvFrom := pod(nil, nil, nil, func(p *corev1.Pod) {
		p.Spec.Containers[0].EnvFrom = []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "x"}}}}
	})
	if r := only(t, Compare(sa(map[string]string{AnnRoleARN: roleA}), withEnvFrom, PlatformOther), AdapterAWS); !strings.Contains(r.Text, "envFrom") {
		t.Fatalf("envFrom said: %+v", r)
	}
}

func TestChains(t *testing.T) {
	declared := sa(map[string]string{AnnRoleARN: roleA})
	cases := []struct {
		name  string
		p     Pod
		state string
	}{
		{"wrong file", pod(env(EnvRoleARN, roleA, EnvWebTokenFile, irsaDir+"/other"), []corev1.VolumeMount{{Name: "aws-iam-token", MountPath: irsaDir}},
			[]corev1.Volume{tokenVol("aws-iam-token", "token", AudienceSTS)}), StatePartial},
		{"unmounted volume", pod(env(EnvRoleARN, roleA, EnvWebTokenFile, irsaDir+"/token"), nil,
			[]corev1.Volume{tokenVol("aws-iam-token", "token", AudienceSTS)}), StatePartial},
		{"not a token volume", pod(env(EnvRoleARN, roleA, EnvWebTokenFile, irsaDir+"/token"), []corev1.VolumeMount{{Name: "cfg", MountPath: irsaDir}},
			[]corev1.Volume{{Name: "cfg", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}), StatePartial},
		{"subPath", pod(env(EnvRoleARN, roleA, EnvWebTokenFile, irsaDir+"/token"), []corev1.VolumeMount{{Name: "aws-iam-token", MountPath: irsaDir + "/token", SubPath: "token"}},
			[]corev1.Volume{tokenVol("aws-iam-token", "token", AudienceSTS)}), StateMatch},
		{"subPathExpr", pod(env(EnvRoleARN, roleA, EnvWebTokenFile, irsaDir+"/token"), []corev1.VolumeMount{{Name: "aws-iam-token", MountPath: irsaDir, SubPathExpr: "$(X)"}},
			[]corev1.Volume{tokenVol("aws-iam-token", "token", AudienceSTS)}), StateUnresolved},
		{"valueFrom", pod([]corev1.EnvVar{{Name: EnvRoleARN, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{Key: "k"}}}, {Name: EnvWebTokenFile, Value: irsaDir + "/token"}},
			[]corev1.VolumeMount{{Name: "aws-iam-token", MountPath: irsaDir}}, []corev1.Volume{tokenVol("aws-iam-token", "token", AudienceSTS)}), StateUnresolved},
		{"expansion", pod(env(EnvRoleARN, "$(ROLE)", EnvWebTokenFile, irsaDir+"/token"), []corev1.VolumeMount{{Name: "aws-iam-token", MountPath: irsaDir}},
			[]corev1.Volume{tokenVol("aws-iam-token", "token", AudienceSTS)}), StateUnresolved},
		{"oversized", pod(env(EnvRoleARN, roleA+strings.Repeat("x", MaxValue), EnvWebTokenFile, irsaDir+"/token"), []corev1.VolumeMount{{Name: "aws-iam-token", MountPath: irsaDir}},
			[]corev1.Volume{tokenVol("aws-iam-token", "token", AudienceSTS)}), StateUnresolved},
		{"half a pair", pod(env(EnvRoleARN, roleA), nil, nil), StatePartial},
		{"windows path", pod(env(EnvRoleARN, roleA, EnvWebTokenFile, `C:\var\run\secrets\eks.amazonaws.com\serviceaccount\token`),
			[]corev1.VolumeMount{{Name: "aws-iam-token", MountPath: `C:\var\run\secrets\eks.amazonaws.com\serviceaccount`}}, []corev1.Volume{tokenVol("aws-iam-token", "token", AudienceSTS)}), StateMatch},
	}
	for _, c := range cases {
		if r := only(t, Compare(declared, c.p, PlatformOther), AdapterAWS); r.State != c.state {
			t.Errorf("%s: %+v", c.name, r)
		}
	}
}

const piDir = "/var/run/secrets/pods.eks.amazonaws.com/serviceaccount"

func piEnv() []corev1.EnvVar {
	return env(EnvCredsURI, "http://169.254.170.23/v1/credentials", EnvCredsToken, piDir+"/eks-pod-identity-token")
}

func TestPodIdentity(t *testing.T) {
	mounts := []corev1.VolumeMount{{Name: "eks-pod-identity-token", MountPath: piDir}}
	vols := []corev1.Volume{tokenVol("eks-pod-identity-token", "eks-pod-identity-token", AudiencePodIdentity)}
	p := pod(piEnv(), mounts, vols)
	r := only(t, Compare(sa(map[string]string{AnnRoleARN: roleA}), p, PlatformOther), AdapterAWS)
	if r.State != StatePodIdentity || !strings.Contains(r.Text, "prefers Pod Identity") || !strings.Contains(r.Text, "not visible") {
		t.Fatalf("pod identity: %+v", r)
	}
	wrongAud := pod(piEnv(), mounts, []corev1.Volume{tokenVol("eks-pod-identity-token", "eks-pod-identity-token", AudienceSTS)})
	if r := only(t, Compare(sa(nil), wrongAud, PlatformOther), AdapterAWS); r.State != StatePartial || !strings.Contains(r.Text, "audience") {
		t.Fatalf("another audience: %+v", r)
	}
	both := pod(append(piEnv(), env(EnvRoleARN, roleA, EnvWebTokenFile, irsaDir+"/token")...), mounts, vols)
	if r := only(t, Compare(sa(nil), both, PlatformOther), AdapterAWS); r.State != StateBothAWS || !strings.Contains(r.Text, "not inferred") {
		t.Fatalf("both mechanisms: %+v", r)
	}
	labelled := pod(piEnv(), mounts, vols, func(p *corev1.Pod) { p.Labels = map[string]string{LabelPodIdentity: "enabled"} })
	if !labelled.PodIdentityLabel || pod(piEnv(), mounts, vols).PodIdentityLabel {
		t.Fatal("the v0.6.15 label is evidence when present, never required")
	}
}

const azDir = "/var/run/secrets/azure/wi/abc123"

func azurePod(client, ten string, opts ...podOpt) Pod {
	opts = append([]podOpt{func(p *corev1.Pod) { p.Labels = map[string]string{LabelAzureUse: "true"} }}, opts...)
	return pod(env(EnvAzureClient, client, EnvAzureTenant, ten, EnvAzureToken, azDir+"/token/azure-identity-token", EnvAzureAuthHost, "https://login.microsoftonline.com/"),
		[]corev1.VolumeMount{{Name: "azure-workload-identity-reserved-abc123", MountPath: azDir}},
		[]corev1.Volume{tokenVol("azure-workload-identity-reserved-abc123", "token/azure-identity-token", AudienceAzure)}, opts...)
}

func TestAzure(t *testing.T) {
	full := map[string]string{AnnAzureClient: guidA, AnnAzureTenant: tenant}
	if r := only(t, Compare(sa(full), azurePod(guidA, tenant), PlatformOther), AdapterAzure); r.State != StateMatch || r.Compared != "client ID and tenant" {
		t.Fatalf("match: %+v", r)
	}
	if r := only(t, Compare(sa(map[string]string{AnnAzureClient: guidA, AnnAzureTenant: guidB}), azurePod(guidA, tenant), PlatformOther), AdapterAzure); r.State != StateDiffers || !strings.Contains(r.Text, "tenant") {
		t.Fatalf("a tenant mismatch: %+v", r)
	}
	if r := only(t, Compare(sa(map[string]string{AnnAzureClient: guidA}), azurePod(guidA, tenant), PlatformOther), AdapterAzure); r.State != StateMatchClient || !strings.Contains(r.Text, "not visible") || strings.Contains(r.Text, "is the webhook's default") {
		t.Fatalf("no tenant declared: the origin is not claimed: %+v", r)
	}
	notOpted := pod(nil, nil, nil, func(p *corev1.Pod) { p.Labels = map[string]string{LabelAzureUse: "True"} })
	if r := only(t, Compare(sa(full), notOpted, PlatformOther), AdapterAzure); r.State != StateNotOptedIn {
		t.Fatalf("the label must be exactly \"true\": %+v", r)
	}
	skipped := pod(nil, nil, nil, func(p *corev1.Pod) {
		p.Labels = map[string]string{LabelAzureUse: "true"}
		p.Annotations = map[string]string{AnnAzureSkip: "x; app"}
	})
	if r := only(t, Compare(sa(full), skipped, PlatformOther), AdapterAzure); r.State != StateExcluded {
		t.Fatalf("skipped (semicolon): %+v", r)
	}
	old := pod(env(EnvAzureClient, guidA, EnvAzureTenant, tenant, EnvAzureToken, "/var/run/secrets/azure/tokens/azure-identity-token"),
		[]corev1.VolumeMount{{Name: "azure-identity-token", MountPath: "/var/run/secrets/azure/tokens"}},
		[]corev1.Volume{tokenVol("azure-identity-token", "azure-identity-token", AudienceAzure)}, func(p *corev1.Pod) { p.Labels = map[string]string{LabelAzureUse: "true"} })
	if r := only(t, Compare(sa(full), old, PlatformOther), AdapterAzure); r.State != StateMatch {
		t.Fatalf("the pre-v1.6 volume: %+v", r)
	}
	if d := sa(map[string]string{AnnAzureClient: guidA, AnnAzureExpiry: "60"}); !d.AzureExpiryOutOfRange {
		t.Fatal("an expiration out of range is said")
	}
}

func TestGKE(t *testing.T) {
	g := "app@proj.iam.gserviceaccount.com"
	if r := only(t, Compare(sa(map[string]string{AnnGKEServiceAcct: g}), pod(nil, nil, nil), PlatformGKE), AdapterGKE); r.State != StateLinked || !strings.Contains(r.Text, g) {
		t.Fatalf("linked: %+v", r)
	}
	if r := only(t, Compare(sa(nil), pod(nil, nil, nil), PlatformGKE), AdapterGKE); r.State != StateUnlinked {
		t.Fatalf("direct federation: %+v", r)
	}
	host := pod(nil, nil, nil, func(p *corev1.Pod) { p.Spec.HostNetwork = true })
	// only GKE's metadata server honours the link: on nodes read as not GKE
	// it does nothing; with the nodes unread, GKE is not known either way
	if r := only(t, Compare(sa(map[string]string{AnnGKEServiceAcct: g}), pod(nil, nil, nil), PlatformOther), AdapterGKE); r.State != StateNotGKE || !strings.Contains(r.Text, "does nothing here") {
		t.Fatalf("a link off GKE: %+v", r)
	}
	if r := only(t, Compare(sa(map[string]string{AnnGKEServiceAcct: g}), pod(nil, nil, nil), PlatformUnread), AdapterGKE); r.State != StateLinked || !strings.Contains(r.Text, "whether this cluster is GKE is not known") {
		t.Fatalf("a link with the nodes unread: %+v", r)
	}
	if rs := Compare(sa(nil), pod(nil, nil, nil), PlatformOther); len(rs) != 0 {
		t.Fatalf("no link and not GKE: no GKE reading: %+v", rs)
	}
	if r := only(t, Compare(sa(map[string]string{AnnGKEServiceAcct: g}), host, PlatformGKE), AdapterGKE); r.State != StateHostNetwork {
		t.Fatalf("hostNetwork: %+v", r)
	}
}

// S5 §7: a value is shown only in its documented shape; a URL through the
// outbound policy. The needles sit in allowed variables.
func TestValuePolicy(t *testing.T) {
	needle := "S5NEEDLE"
	p := pod(env(EnvRoleARN, "arn:aws:iam::123456789012:role/x?"+needle, EnvWebTokenFile, irsaDir+"/token",
		EnvCredsURI, "http://user:"+needle+"@169.254.170.23/v1/credentials", EnvAzureClient, needle, EnvAzureAuthHost, "https://login.example.com/"+needle+"?x=1",
		"OTHER_SECRET", needle), nil, nil)
	raw, _ := json.Marshal(p)
	if strings.Contains(string(raw), needle) {
		t.Fatalf("a needle crossed: %s", raw)
	}
	if v := valueOf("http://[fd00:ec2::23]/v1/credentials", shapeURL); v.Unexpected || v.Shown != "http://[fd00:ec2::23]/v1/credentials" {
		t.Fatalf("a documented URL is shown: %+v", v)
	}
	if v := valueOf("https://login.microsoftonline.com/", shapeURL); v.Unexpected {
		t.Fatalf("the authority host: %+v", v)
	}
	if v := valueOf("https://u:p@login.microsoftonline.com/", shapeURL); !v.Unexpected || strings.Contains(v.Shown, "u:p") {
		t.Fatalf("user info removed: %+v", v)
	}
	if _, ok := p.Containers[0].Env["OTHER_SECRET"]; ok {
		t.Fatal("a variable outside the allowlist is dropped")
	}
	// an unexpected value still compares by its exact bytes
	odd := "not-an-arn"
	if r := only(t, Compare(sa(map[string]string{AnnRoleARN: odd}), irsaPod(odd), PlatformOther), AdapterAWS); r.State != StateMatch || strings.Contains(r.Text, odd) {
		t.Fatalf("exact comparison, never shown: %+v", r)
	}
}

func TestBounds(t *testing.T) {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "big"}}
	for i := 0; i < 20; i++ {
		p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: "c"})
		p.Spec.Volumes = append(p.Spec.Volumes, tokenVol("v", "t", AudienceSTS))
	}
	got := PodOf(p)
	if len(got.Containers) != MaxContainers || got.ContainersCut != 4 || !got.VolumesCut {
		t.Fatalf("bounds: %d %d %v", len(got.Containers), got.ContainersCut, got.VolumesCut)
	}
}

func TestNodeSide(t *testing.T) {
	auto := &NodeInfo{Name: "n1", Labels: map[string]string{LabelComputeType: "auto"}}
	if s := PodIdentityNode(auto, nil, true); s.Management.Manager != capability.ManagerPlatform || s.Management.Observed != capability.NotObserved || !strings.Contains(s.Health, "not directly observed") {
		t.Fatalf("Auto Mode: %+v", s)
	}
	ec2 := &NodeInfo{Name: "n1", Labels: map[string]string{}}
	if s := PodIdentityNode(ec2, []ComponentPod{{Name: "agent-x", Node: "n1", Ready: false}}, true); !s.Fault || s.Management.Observed != capability.Observed {
		t.Fatalf("an observed agent not ready is never suppressed: %+v", s)
	}
	if s := PodIdentityNode(ec2, nil, true); s.Health != "no Pod Identity agent pod on this node" || !s.Fault {
		t.Fatalf("no agent: %+v", s)
	}
	if s := PodIdentityNode(&NodeInfo{Name: "n1", Labels: map[string]string{LabelComputeType: "fargate"}}, nil, true); !strings.Contains(s.Note, "Fargate") || s.Fault {
		t.Fatalf("fargate: %+v", s)
	}
	win := &NodeInfo{Name: "w1", OS: "windows", ProviderID: "gce://p/z/w1"}
	if s := GKENode(win, nil, true); s.Management.Observed != capability.NotObserved || !strings.Contains(s.Health, "Windows service") {
		t.Fatalf("GKE Windows: %+v", s)
	}
	lin := &NodeInfo{Name: "l1", ProviderID: "gce://p/z/l1", Labels: map[string]string{LabelGKEMetadata: "true"}}
	if s := GKENode(lin, []ComponentPod{{Name: "gke-metadata-server-a", Node: "l1", Ready: true}}, true); s.Management.Manager != capability.ManagerPlatform || s.Fault {
		t.Fatalf("GKE Linux: %+v", s)
	}
	if s := WebhookSide(Webhook{Name: "pod-identity-webhook", Present: true, Read: true, URL: true}); s.Management.Observed != capability.NotObserved || s.Management.Manager != capability.ManagerUnknown {
		t.Fatalf("a URL-served webhook: %+v", s)
	}
	if s := WebhookSide(Webhook{Name: "azure-wi", Present: true, Read: true, Service: "kube-system/x", FailClosed: true, Selectors: true}); !strings.Contains(s.Note, "selectors match") {
		t.Fatalf("Azure's consequence: %+v", s)
	}
}

func store(kind, ns string, provider map[string]any, extra ...func(map[string]any)) *unstructured.Unstructured {
	spec := map[string]any{"provider": provider}
	for _, e := range extra {
		e(spec)
	}
	u := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "external-secrets.io/v1", "kind": kind,
		"metadata": map[string]any{"name": "s", "namespace": ns}, "spec": spec}}
	if kind == KindClusterSecretStore {
		unstructured.RemoveNestedField(u.Object, "metadata", "namespace")
	}
	return u
}

func lookup(sas map[string]map[string]string) SALookup {
	return func(ns, name string) (*corev1.ServiceAccount, Read) {
		a, ok := sas[ns+"/"+name]
		if !ok {
			return nil, ReadMissing
		}
		return &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Annotations: a}}, ReadFound
	}
}

func TestESOAWS(t *testing.T) {
	jwt := func(roles ...any) map[string]any {
		p := map[string]any{"auth": map[string]any{"jwt": map[string]any{"serviceAccountRef": map[string]any{"name": "eso"}}}}
		if len(roles) > 0 {
			p["additionalRoles"] = []any{"arn:aws:iam::123456789012:role/b", "arn:aws:iam::123456789012:role/c"}
			p["role"] = "arn:aws:iam::123456789012:role/d"
		}
		return map[string]any{"aws": p}
	}
	s := StoreSubject(store(KindSecretStore, "app", jwt(true)), lookup(map[string]map[string]string{"app/eso": {AnnRoleARN: roleA}}))
	want := []string{roleA, "arn:aws:iam::123456789012:role/b", "arn:aws:iam::123456789012:role/c", "arn:aws:iam::123456789012:role/d"}
	if strings.Join(s.Requested, ",") != strings.Join(want, ",") {
		t.Fatalf("the chain A, B, C, D: %v", s.Requested)
	}
	s = StoreSubject(store(KindSecretStore, "app", jwt()), lookup(map[string]map[string]string{"app/eso": {}}))
	if len(s.Refusals) != 1 || !strings.Contains(s.Refusals[0], "role-arn") {
		t.Fatalf("no role-arn: ESO refuses: %+v", s)
	}
	none := StoreSubject(store(KindSecretStore, "app", map[string]any{"aws": map[string]any{"additionalRoles": []any{"arn:aws:iam::123456789012:role/b"}, "role": "arn:aws:iam::123456789012:role/d"}}), nil)
	if !none.Controller || strings.Join(none.Requested, ",") != "arn:aws:iam::123456789012:role/b,arn:aws:iam::123456789012:role/d" {
		t.Fatalf("no auth: the controller's chain, then the roles: %+v", none)
	}
	sec := StoreSubject(store(KindSecretStore, "app", map[string]any{"aws": map[string]any{"auth": map[string]any{"secretRef": map[string]any{}}, "role": "arn:aws:iam::123456789012:role/d"}}), nil)
	if sec.Mode != "secretRef" || len(sec.Requested) != 1 {
		t.Fatalf("static keys then the role: %+v", sec)
	}
	badNS := StoreSubject(store(KindSecretStore, "app", map[string]any{"aws": map[string]any{"auth": map[string]any{"jwt": map[string]any{"serviceAccountRef": map[string]any{"name": "eso", "namespace": "other"}}}}}),
		lookup(map[string]map[string]string{"app/eso": {AnnRoleARN: roleA}}))
	if len(badNS.Refusals) == 0 {
		t.Fatalf("a namespaced store's other namespace: %+v", badNS)
	}
	ref := StoreSubject(store(KindClusterSecretStore, "", map[string]any{"aws": map[string]any{"auth": map[string]any{"jwt": map[string]any{"serviceAccountRef": map[string]any{"name": "eso"}}}}}), nil)
	if ref.SA == nil || !ref.SA.Referent {
		t.Fatalf("a ClusterSecretStore without a namespace is referent: %+v", ref)
	}
}

func azStore(fields map[string]any) *unstructured.Unstructured {
	p := map[string]any{"authType": "WorkloadIdentity", "serviceAccountRef": map[string]any{"name": "eso"}}
	for k, v := range fields {
		p[k] = v
	}
	return store(KindSecretStore, "app", map[string]any{"azurekv": p})
}

func TestESOAzure(t *testing.T) {
	both := lookup(map[string]map[string]string{"app/eso": {AnnAzureClient: guidA, AnnAzureTenant: tenant}})
	clientOnly := lookup(map[string]map[string]string{"app/eso": {AnnAzureClient: guidA}})
	refused := func(s Subject, want string) bool {
		for _, r := range s.Refusals {
			if strings.Contains(r, want) {
				return true
			}
		}
		return false
	}
	// legacy: the store tenant beside the annotation, equal or not, is refused
	if s := StoreSubject(azStore(map[string]any{"tenantId": tenant}), both); !refused(s, "both set") {
		t.Fatalf("store tenant + annotation: %+v", s)
	}
	// an empty store tenant falls through to the annotation
	if s := StoreSubject(azStore(map[string]any{"tenantId": ""}), both); len(s.Refusals) != 0 {
		t.Fatalf("an empty store tenant is no duplicate: %+v", s)
	}
	// an authSecretRef without clientId is refused before the annotation
	for _, ref := range []map[string]any{{}, {"tenantId": map[string]any{"name": "s", "key": "t"}}} {
		if s := StoreSubject(azStore(map[string]any{"authSecretRef": ref}), clientOnly); !refused(s, "no clientId") {
			t.Fatalf("authSecretRef %v: %+v", ref, s)
		}
	}
	// a Secret-sourced client beside the annotation is never claimed
	if s := StoreSubject(azStore(map[string]any{"authSecretRef": map[string]any{"clientId": map[string]any{"name": "s", "key": "c"}}}), clientOnly); len(s.Refusals) != 0 || len(s.Unresolved) == 0 {
		t.Fatalf("value-dependent: %+v", s)
	}
	// a Secret-sourced tenant beside the annotation is never claimed
	if s := StoreSubject(azStore(map[string]any{"tenantId": tenant, "authSecretRef": map[string]any{"clientId": map[string]any{"name": "s", "key": "c"}, "tenantId": map[string]any{"name": "s", "key": "t"}}}), both); refused(s, "both set") {
		t.Fatalf("a Secret tenant: %+v", s)
	}
	// no client anywhere
	if s := StoreSubject(azStore(nil), lookup(map[string]map[string]string{"app/eso": {}})); !refused(s, "no client ID") {
		t.Fatalf("no client: %+v", s)
	}
	// the new SDK prefers the store tenant, an empty one used as is
	if s := StoreSubject(azStore(map[string]any{"useAzureSDK": true, "tenantId": tenant}), both); len(s.Refusals) != 0 {
		t.Fatalf("new SDK: no duplicate refusal: %+v", s)
	}
	if s := StoreSubject(azStore(map[string]any{"useAzureSDK": true, "tenantId": ""}), both); !strings.Contains(strings.Join(s.Notes, " "), "used as is") {
		t.Fatalf("new SDK empty tenant: %+v", s)
	}
	if s := StoreSubject(azStore(map[string]any{"useAzureSDK": true}), lookup(map[string]map[string]string{"app/eso": {AnnAzureTenant: tenant}})); !refused(s, "no "+AnnAzureClient) {
		t.Fatalf("new SDK: client from the annotation only: %+v", s)
	}
	// no serviceAccountRef: the controller's environment
	noRef := store(KindSecretStore, "app", map[string]any{"azurekv": map[string]any{"authType": "WorkloadIdentity"}})
	if s := StoreSubject(noRef, nil); !s.ControllerEnv {
		t.Fatalf("the controller-env branch: %+v", s)
	}
}

func TestESOGCP(t *testing.T) {
	sas := lookup(map[string]map[string]string{"app/eso": {AnnGKEServiceAcct: "linked@p.iam.gserviceaccount.com"}})
	wi := store(KindSecretStore, "app", map[string]any{"gcpsm": map[string]any{"auth": map[string]any{"workloadIdentity": map[string]any{"serviceAccountRef": map[string]any{"name": "eso"}}}}})
	if s := StoreSubject(wi, sas); len(s.Requested) != 1 || s.Requested[0] != "linked@p.iam.gserviceaccount.com" {
		t.Fatalf("workloadIdentity: %+v", s)
	}
	wif := store(KindSecretStore, "app", map[string]any{"gcpsm": map[string]any{"auth": map[string]any{"workloadIdentityFederation": map[string]any{
		"serviceAccountRef": map[string]any{"name": "eso"}, "gcpServiceAccountEmail": "override@p.iam.gserviceaccount.com", "audience": "x"}}}})
	if s := StoreSubject(wif, sas); len(s.Requested) != 1 || s.Requested[0] != "override@p.iam.gserviceaccount.com" {
		t.Fatalf("gcpServiceAccountEmail overrides: %+v", s)
	}
	if s := StoreSubject(store(KindSecretStore, "app", map[string]any{"gcpsm": map[string]any{}}), nil); !s.Controller {
		t.Fatalf("no auth: ADC: %+v", s)
	}
}

func ctlPod(name, saName, class string, unresolved bool) ControllerPod {
	return ControllerPod{Pod: Pod{Namespace: "eso", Name: name, ServiceAccount: saName}, Class: class, ClassUnresolved: unresolved, Owned: true}
}

func TestHandlers(t *testing.T) {
	// a class A to B rollout: old pods run A as SA_A, new ones B as SA_B
	rollout := []Candidate{{Namespace: "eso", Name: "external-secrets", PodsComplete: true,
		Pods: []ControllerPod{ctlPod("old", "sa-a", "a", false), ctlPod("new", "sa-b", "b", false)}}}
	if h := HandlersOf("b", rollout, true); len(h.Groups) != 1 || h.Groups[0].ServiceAccount != "sa-b" {
		t.Fatalf("a class-B store: %+v", h)
	}
	if h := HandlersOf("a", rollout, true); len(h.Groups) != 1 || h.Groups[0].ServiceAccount != "sa-a" {
		t.Fatalf("a class-A store: %+v", h)
	}
	if h := HandlersOf("", rollout, true); len(h.Groups) != 2 {
		t.Fatalf("an empty class accepts both: %+v", h)
	}
	if h := HandlersOf("b", []Candidate{{PodsComplete: false}}, true); h.Unresolved == "" {
		t.Fatalf("a capped pod read certifies nothing: %+v", h)
	}
	if h := HandlersOf("b", rollout, false); h.Unresolved == "" {
		t.Fatalf("an incomplete candidate read certifies nothing: %+v", h)
	}
	unres := []Candidate{{PodsComplete: true, Pods: []ControllerPod{ctlPod("p", "sa", "", true)}}}
	if h := HandlersOf("b", unres, true); h.Unresolved == "" {
		t.Fatalf("an unresolved class: %+v", h)
	}
	if h := HandlersOf("", unres, true); len(h.Groups) != 1 {
		t.Fatalf("an empty store class needs no class: %+v", h)
	}
	if h := HandlersOf("z", rollout, true); h.Unresolved == "" || len(h.Groups) != 0 {
		t.Fatalf("no applicable pod: %+v", h)
	}
	if c, ok := ClassOf(corev1.Container{Args: []string{"--controller-class=b"}}); !ok || c != "b" {
		t.Fatal("class arg")
	}
	if c, ok := ClassOf(corev1.Container{Args: []string{"--controller-class", "c"}}); !ok || c != "c" {
		t.Fatal("class arg (split)")
	}
	if _, ok := ClassOf(corev1.Container{Args: []string{"--controller-class=$(CLASS)"}}); ok {
		t.Fatal("an expanding class is unresolved")
	}
	if c, _ := ClassOf(corev1.Container{}); c != "default" {
		t.Fatal("the default class")
	}
	g := Group{Pods: []Pod{{Name: "p", Containers: []Container{{Name: "c", Env: map[string]EnvValue{EnvAzureClient: {}}}}}}}
	if gaps := ControllerEnvGaps(g); len(gaps) != 2 || !strings.Contains(gaps[0], "envFrom") {
		t.Fatalf("the controller's env gaps: %v", gaps)
	}
}
