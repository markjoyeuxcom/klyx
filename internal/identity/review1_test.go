package identity

import (
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/moomora/klyx/internal/capability"
)

// Implementation review 1 (S5): each finding's regression.

// #1: a token entry's audience compares by its exact bytes and is shown
// only in a token's shape: complete, wrong-audience and oversized chains,
// through the pod, ServiceAccount and handler reports.
func TestReview1AudienceIsOutboundSafe(t *testing.T) {
	const needle = "S5R1audienceNeedle"
	withAud := func(aud string) func(*corev1.Pod) {
		return func(p *corev1.Pod) { p.Spec.Volumes[0].Projected.Sources[0].ServiceAccountToken.Audience = aud }
	}
	cases := map[string]Pod{
		"complete":  irsaPod(roleA, withAud("https://u:"+needle+"@issuer.example/token")),
		"oversized": irsaPod(roleA, withAud(strings.Repeat(needle, 100))),
		"wrong audience": pod(env(EnvCredsURI, "http://169.254.170.23/v1/credentials", EnvCredsToken, "/pi/token"),
			[]corev1.VolumeMount{{Name: "pi", MountPath: "/pi"}}, []corev1.Volume{tokenVol("pi", "token", "https://"+needle+"@x/")}),
	}
	for name, p := range cases {
		pr := PodReportOf(sa(map[string]string{AnnRoleARN: roleA}), p, Env{})
		docs := []any{pr, SAReport{Pods: []PodReport{pr}}, StoreReport{Handlers: &Handlers{Groups: []Group{{Pods: []Pod{p}}}}}}
		for _, d := range docs {
			b, _ := json.Marshal(d)
			if strings.Contains(string(b), needle) {
				t.Fatalf("%s: the raw audience crossed: %s", name, b)
			}
		}
	}
	// it still compares exactly: a matching custom audience matches
	custom := irsaPod(roleA, withAud("custom-aud"))
	if r := only(t, Compare(sa(map[string]string{AnnRoleARN: roleA, AnnAudience: "custom-aud"}), custom, PlatformOther), AdapterAWS); r.State != StateMatch {
		t.Fatalf("exact audience comparison: %+v", r)
	}
}

// #4: gce:// proves Google Compute, not GKE; GKE needs its node-pool label
// or a metadata-server pod, and the manager is the platform only on the
// metadata server's pod (C2).
func TestReview1GCEIsNotGKE(t *testing.T) {
	g := "app@p.iam.gserviceaccount.com"
	gce := &NodeInfo{Name: "vm", ProviderID: "gce://p/z/vm", Labels: map[string]string{}}
	e := Env{NodesRead: true, Nodes: map[string]*NodeInfo{"vm": gce}, MetadataRead: true}
	r := PodReportOf(sa(map[string]string{AnnGKEServiceAcct: g}), Pod{Node: "vm"}, e)
	if len(r.Nodes) != 0 {
		t.Fatalf("a Google Compute node without GKE's evidence reads no GKE node side: %+v", r.Nodes)
	}
	if x := only(t, r.Readings, AdapterGKE); x.State != StateLinked || !strings.Contains(x.Text, "not verified: its Google Compute (gce://) nodes carry no") {
		t.Fatalf("unverified, said: %+v", x)
	}
	// GKE's node-pool label proves GKE; without a metadata-server pod the
	// manager stays unknown
	gke := &NodeInfo{Name: "vm", ProviderID: "gce://p/z/vm", Labels: map[string]string{LabelGKENodePool: "pool-1"}}
	e.Nodes["vm"] = gke
	r = PodReportOf(sa(map[string]string{AnnGKEServiceAcct: g}), Pod{Node: "vm"}, e)
	if len(r.Nodes) != 1 || r.Nodes[0].Management.Manager != capability.ManagerUnknown {
		t.Fatalf("no metadata-server pod: manager unknown: %+v", r.Nodes)
	}
	e.MetadataPods = []ComponentPod{{Name: "gke-metadata-server-x", Node: "vm", Ready: true}}
	r = PodReportOf(sa(map[string]string{AnnGKEServiceAcct: g}), Pod{Node: "vm"}, e)
	if len(r.Nodes) != 1 || r.Nodes[0].Management.Manager != capability.ManagerPlatform || only(t, r.Readings, AdapterGKE).State != StateLinked {
		t.Fatalf("the metadata server's pod on a gce:// node: %+v", r)
	}
	// a node without a provider ID settles nothing
	e = Env{NodesRead: true, Nodes: map[string]*NodeInfo{"n": {Name: "n"}}}
	if x := only(t, PodReportOf(sa(map[string]string{AnnGKEServiceAcct: g}), Pod{Node: "n"}, e).Readings, AdapterGKE); x.State != StateLinked || !strings.Contains(x.Text, "a node has no provider ID") {
		t.Fatalf("no provider ID: %+v", x)
	}
}

// #5: gcpServiceAccountEmail applies after every federation base.
func TestReview1GCPOverrideEveryBase(t *testing.T) {
	const email = "override@p.iam.gserviceaccount.com"
	bases := map[string]any{
		"credConfig":             map[string]any{"name": "cfg", "key": "credentials.json"},
		"awsSecurityCredentials": map[string]any{"region": "us-east-1"},
		"serviceAccountRef":      map[string]any{"name": "eso"},
	}
	for base, v := range bases {
		s := store(KindSecretStore, "app", map[string]any{"gcpsm": map[string]any{"auth": map[string]any{"workloadIdentityFederation": map[string]any{
			base: v, "audience": "aud", "gcpServiceAccountEmail": email}}}})
		sub := StoreSubject(s, lookup(map[string]map[string]string{"app/eso": {AnnGKEServiceAcct: "linked@p.iam.gserviceaccount.com"}}))
		if len(sub.Requested) != 1 || sub.Requested[0] != email {
			t.Errorf("%s: the override is the requested principal: %+v", base, sub)
		}
	}
}

// #6: a drive is part of a Windows path; a token entry past the bound in a
// partly read volume is unresolved; "configured" only for complete chains.
func TestReview1ChainEvidence(t *testing.T) {
	win := func(p *corev1.Pod) { p.Spec.OS = &corev1.PodOS{Name: corev1.Windows} }
	d := pod(env(EnvRoleARN, roleA, EnvWebTokenFile, `D:\tokens\token`), []corev1.VolumeMount{{Name: "aws", MountPath: `C:\tokens`}}, []corev1.Volume{tokenVol("aws", "token", AudienceSTS)}, win)
	if m := d.Containers[0].Mechanisms[0]; m.State != ChainPartial {
		t.Fatalf("D: never resolves through C: %+v", m)
	}
	c := pod(env(EnvRoleARN, roleA, EnvWebTokenFile, `C:\tokens\token`), []corev1.VolumeMount{{Name: "aws", MountPath: `\tokens`}}, []corev1.Volume{tokenVol("aws", "token", AudienceSTS)}, win)
	if m := c.Containers[0].Mechanisms[0]; m.State != ChainComplete {
		t.Fatalf("a path without a drive is on C: %+v", m)
	}
	v := tokenVol("aws", "t0", AudienceSTS)
	for i := 1; i <= MaxVolumeSources; i++ {
		v.Projected.Sources = append(v.Projected.Sources, corev1.VolumeProjection{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Path: "other", Audience: AudienceSTS}})
	}
	v.Projected.Sources[MaxVolumeSources].ServiceAccountToken.Path = "last"
	cut := pod(env(EnvRoleARN, roleA, EnvWebTokenFile, "/tokens/last"), []corev1.VolumeMount{{Name: "aws", MountPath: "/tokens"}}, []corev1.Volume{v})
	if m := cut.Containers[0].Mechanisms[0]; !cut.VolumesCut || m.State != ChainUnresolved {
		t.Fatalf("an entry past the bound is unresolved: %+v", m)
	}
	plain := pod(env(EnvRoleARN, roleA, EnvWebTokenFile, "/tokens/x"), []corev1.VolumeMount{{Name: "cm", MountPath: "/tokens"}},
		[]corev1.Volume{{Name: "cm", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{}}}, v})
	if m := plain.Containers[0].Mechanisms[0]; m.State != ChainPartial || !strings.Contains(m.Missing, "not a projected token volume") {
		t.Fatalf("a volume that is not projected is definite even with a cut elsewhere: %+v", m)
	}
	both := pod(env(EnvRoleARN, roleA, EnvCredsURI, "http://169.254.170.23/v1/credentials"), nil, nil)
	if r := only(t, Compare(sa(nil), both, PlatformOther), AdapterAWS); r.State != StatePartial || strings.Contains(r.Text, "configured") {
		t.Fatalf("two incomplete mechanisms: %+v", r)
	}
	half := pod(append(env(EnvCredsURI, "http://169.254.170.23/v1/credentials"), env(EnvRoleARN, roleA, EnvWebTokenFile, irsaDir+"/token")...),
		[]corev1.VolumeMount{{Name: "aws-iam-token", MountPath: irsaDir}}, []corev1.Volume{tokenVol("aws-iam-token", "token", AudienceSTS)})
	if r := only(t, Compare(sa(nil), half, PlatformOther), AdapterAWS); r.State != StateBothAWS || !strings.Contains(r.Text, "IRSA is configured; EKS Pod Identity is also present but partial") {
		t.Fatalf("one complete, one partial: %+v", r)
	}
}

// #7: the last --controller-class wins, "--" ends the flags; a departed
// pod names no handler, and one not running yet is said, never counted.
func TestReview1ControllerClassAndPhase(t *testing.T) {
	for args, want := range map[string]string{"--controller-class=old --controller-class=new": "new", "--controller-class old --controller-class=new": "new",
		"--controller-class=a -- --controller-class=b": "a", "": "default"} {
		got, ok := ClassOf(corev1.Container{Args: strings.Fields(args)})
		if !ok || got != want {
			t.Errorf("%q: %q, want %q", args, got, want)
		}
	}
	gone := ctlPod("old", "sa-old", "", false)
	gone.Phase = string(corev1.PodSucceeded)
	pending := ctlPod("new", "sa-new", "", false)
	pending.Phase = string(corev1.PodPending)
	h := HandlersOf("", []Candidate{{Namespace: "eso", Name: "eso", PodsComplete: true, Pods: []ControllerPod{gone, pending}}}, true)
	if len(h.Groups) != 0 || h.Unresolved == "" || len(h.NotRunning) != 1 || !strings.Contains(h.NotRunning[0], "new (Pending)") {
		t.Fatalf("no running handler: %+v", h)
	}
	run := ctlPod("run", "sa-run", "", false)
	h = HandlersOf("", []Candidate{{Namespace: "eso", Name: "eso", PodsComplete: true, Pods: []ControllerPod{gone, pending, run}}}, true)
	if len(h.Groups) != 1 || h.Groups[0].ServiceAccount != "sa-run" || len(h.NotRunning) != 1 {
		t.Fatalf("the running pod names its group; the pending one is said: %+v", h)
	}
}
