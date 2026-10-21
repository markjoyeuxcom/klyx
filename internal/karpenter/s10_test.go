package karpenter

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/health"
)

// Coverage S10 §9, the pure package.

// The identifier needles (§9): an ARM ID, an ARN, AWS resource IDs, and a
// bare instance-profile name no pattern can recognise.
const (
	needleARM     = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg-needle/providers/Microsoft.Network/virtualNetworks/vnet/subnets/snet-needle"
	needleARN     = "arn:aws:iam::123456789012:role/NeedleRole"
	needleSubnet  = "subnet-0a1b2c3d4e5f60718"
	needleSG      = "sg-0a1b2c3d4e5f60718"
	needleAMI     = "ami-0a1b2c3d4e5f60718"
	needleProfile = "ExamplePrivateProfileNeedle"
)

var needles = []string{needleARM, needleARN, needleSubnet, needleSG, needleAMI, needleProfile}

func c(t, status, reason, message string, og int64) map[string]any {
	m := map[string]any{"type": t, "status": status, "reason": reason, "message": message,
		"lastTransitionTime": "2026-10-20T09:00:00Z"}
	if og != 0 {
		m["observedGeneration"] = og
	}
	return m
}

func obj(apiVersion, kind, name string, gen int64, spec, status map[string]any, conds ...map[string]any) *unstructured.Unstructured {
	cs := make([]any, 0, len(conds))
	for _, x := range conds {
		cs = append(cs, x)
	}
	if status == nil {
		status = map[string]any{}
	}
	status["conditions"] = cs
	if spec == nil {
		spec = map[string]any{}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion, "kind": kind,
		"metadata": map[string]any{"name": name, "uid": "uid-" + name, "generation": gen,
			"creationTimestamp": "2026-10-20T08:00:00Z"},
		"spec": spec, "status": status,
	}}
}

func ec2(name string, gen int64, conds ...map[string]any) *NodeClass {
	nc := ParseNodeClass(obj("karpenter.k8s.aws/v1", "EC2NodeClass", name, gen, nil, nil, conds...), AdapterEC2)
	return &nc
}

func poolU(gen int64, ref map[string]any, ncog int64, conds ...map[string]any) *unstructured.Unstructured {
	spec := map[string]any{"template": map[string]any{"spec": map[string]any{}}}
	if ref != nil {
		spec["template"].(map[string]any)["spec"].(map[string]any)["nodeClassRef"] = ref
	}
	status := map[string]any{}
	if ncog != 0 {
		status["nodeClassObservedGeneration"] = ncog
	}
	return obj("karpenter.sh/v1", "NodePool", "workers", gen, spec, status, conds...)
}

var ec2Ref = map[string]any{"group": "karpenter.k8s.aws", "kind": "EC2NodeClass", "name": "default"}

func found(class *NodeClass) PoolInputs {
	k, _ := KindFor(class.Adapter)
	return PoolInputs{Link: Link{State: LinkFound, Kind: k, Class: class}}
}

func roles(v PoolVerdict) string {
	var b []string
	for _, f := range v.Findings {
		s := f.Role.String() + " " + f.Label
		if f.Note {
			s = "note " + f.Label
		}
		b = append(b, s)
	}
	return strings.Join(b, "; ")
}

// ---- wording -------------------------------------------------------------

func TestReasonTokenIsTheSchemaPattern(t *testing.T) {
	for _, ok := range []string{"", "SubnetsNotFound", "A", "Reason_1", "A,B:C_d", "x9"} {
		if _, good := ReasonToken(ok); !good {
			t.Errorf("%q is a reason token", ok)
		}
	}
	for _, bad := range []string{"has space", "a/b", "sub-net", "9Lead", "Trailing:", "Trailing,", "_lead", needleSubnet, needleProfile + " x",
		strings.Repeat("A", maxToken+1)} {
		if r, good := ReasonToken(bad); good || r != "" {
			t.Errorf("%q is withheld, got %q %v", bad, r, good)
		}
	}
	for reason := range sentences {
		if _, good := ReasonToken(reason); !good || reason == "" {
			t.Errorf("table key %q is not a token", reason)
		}
	}
}

func TestPhraseNeverReadsTheMessage(t *testing.T) {
	cond := &Cond{Type: "ValidationSucceeded", Status: "False", Reason: "InstanceProfileNotFound",
		Message: `Instance profile "` + needleProfile + `" not found`}
	if got := Phrase(cond); got != "ValidationSucceeded False: InstanceProfileNotFound: the instance profile was not found" {
		t.Fatalf("phrase %q", got)
	}
	if got := Native(cond); !strings.Contains(got, needleProfile) {
		t.Fatalf("the native text keeps the provider's words for the local inspector: %q", got)
	}
	withheld := &Cond{Type: "SubnetsReady", Status: "False", ReasonWithheld: true, Message: needleARM}
	if got := Phrase(withheld); got != "SubnetsReady False: reason withheld: "+Unworded {
		t.Fatalf("withheld %q", got)
	}
	if got := Phrase(&Cond{Type: "Ready", Status: "False", Reason: "SomethingNew"}); got != "Ready False: SomethingNew: "+Unworded {
		t.Fatalf("unknown reason %q", got)
	}
	if got := Phrase(&Cond{Type: "Ready", Status: "False"}); got != "Ready False: "+Unworded {
		t.Fatalf("empty reason %q", got)
	}
}

func TestCondsOfWithholdsANonTokenReason(t *testing.T) {
	cl := ec2("x", 1, c("Ready", "False", "subnet "+needleSubnet, "m", 1))
	if cl.Ready.Reason != "" || !cl.Ready.ReasonWithheld {
		t.Fatalf("a reason outside the pattern is withheld: %+v", cl.Ready)
	}
}

// ---- the class reading (§4) ---------------------------------------------

func TestClassReadingNamesTheFirstFailingDependentInProviderOrder(t *testing.T) {
	cl := ec2("default", 2,
		c("SubnetsReady", "False", "SubnetsNotFound", "no subnets for "+needleSubnet, 2),
		c("AMIsReady", "False", "AMINotFound", "no "+needleAMI, 2),
		c("Ready", "False", "UnhealthyDependents", "", 2))
	r := cl.Read()
	if r.State != ClassNotReady || r.Text != "AMIsReady False: AMINotFound: "+Sentence("AMINotFound") || !strings.Contains(r.Native, needleAMI) {
		t.Fatalf("ec2 orders AMIsReady first: %+v", r)
	}
	aks := ParseNodeClass(obj("karpenter.azure.com/v1beta1", "AKSNodeClass", "default", 1, nil, nil,
		c("SubnetsReady", "False", "SubnetNotFound", "subnet "+needleARM+" not found", 1),
		c("KubernetesVersionReady", "False", "KubernetesVersionUnsupported", "", 1),
		c("Ready", "False", "UnhealthyDependents", "", 1)), AdapterAKS)
	if r := aks.Read(); r.State != ClassNotReady || !strings.HasPrefix(r.Text, "KubernetesVersionReady False") {
		t.Fatalf("aks orders KubernetesVersionReady before SubnetsReady: %+v", r)
	}
	// Auto Mode's set is never assumed: a False type outside it is named.
	auto := ParseNodeClass(obj("eks.amazonaws.com/v1", "NodeClass", "default", 1, nil, nil,
		c("FutureThingReady", "False", "Broken", "", 1),
		c("Ready", "False", "UnhealthyDependents", "", 1)), AdapterEKSAuto)
	if r := auto.Read(); !strings.HasPrefix(r.Text, "FutureThingReady False: Broken") || auto.UnknownTotal != 1 || auto.Unknown[0] != "FutureThingReady" {
		t.Fatalf("auto mode: an unknown False type is named and listed: %+v %+v", r, auto)
	}
	authEntry := ParseNodeClass(obj("eks.amazonaws.com/v1", "NodeClass", "default", 1, nil, nil,
		c("InstanceProfileReady", "False", "UnauthorizedNodeRole", "role "+needleARN, 1),
		c("Ready", "False", "UnhealthyDependents", "", 1)), AdapterEKSAuto)
	if r := authEntry.Read(); r.Text != "InstanceProfileReady False: UnauthorizedNodeRole: the node role has no EKS access entry" {
		t.Fatalf("auto mode access entry: %+v", r)
	}
	// Ready False with no False dependent reads Ready itself.
	if r := ec2("x", 1, c("Ready", "False", "Odd", "", 1)).Read(); !strings.HasPrefix(r.Text, "Ready False: Odd") {
		t.Fatalf("ready itself: %+v", r)
	}
}

func TestClassReadingTerminatingSyncingUnknown(t *testing.T) {
	u := obj("karpenter.k8s.aws/v1", "EC2NodeClass", "gone", 1, nil, nil, c("Ready", "True", "", "", 1))
	u.SetDeletionTimestamp(nil)
	meta := u.Object["metadata"].(map[string]any)
	meta["deletionTimestamp"] = "2026-10-20T09:30:00Z"
	if cl := ParseNodeClass(u, AdapterEC2); cl.Read().State != ClassTerminating {
		t.Fatalf("terminating: %+v", cl.Read())
	}
	// Behind its generation: syncing, never its old Ready (either way).
	if r := ec2("x", 3, c("Ready", "False", "UnhealthyDependents", "", 2), c("SubnetsReady", "False", "SubnetsNotFound", "", 2)).Read(); r.State != ClassSyncing {
		t.Fatalf("old False is syncing: %+v", r)
	}
	if r := ec2("x", 3, c("Ready", "True", "", "", 2)).Read(); r.State != ClassSyncing {
		t.Fatalf("old True is syncing: %+v", r)
	}
	// A failing dependent behind the generation is historical: a current
	// Ready False is read by its own reason, the old dependent beside it.
	if r := ec2("x", 3, c("Ready", "False", "UnhealthyDependents", "", 3), c("SubnetsReady", "False", "SubnetsNotFound", "old", 2)).Read(); r.State != ClassNotReady ||
		r.Text != "Ready False: UnhealthyDependents: a condition it depends on is not ready" ||
		r.Note != "SubnetsReady False, reported for generation 2 (the NodeClass is at 3): SubnetsNotFound: no subnets matched the selector terms" {
		t.Fatalf("an old failing dependent is historical: %+v", r)
	}
	// An unreported generation is read as reported.
	if r := ec2("x", 3, c("Ready", "True", "", "", 0)).Read(); r.State != ClassReady {
		t.Fatalf("unversioned is read as reported: %+v", r)
	}
	if r := ec2("x", 1).Read(); r.State != ClassUnknown {
		t.Fatalf("no conditions: %+v", r)
	}
	if r := ec2("x", 1, c("Ready", "Unknown", "AwaitingReconciliation", "", 1)).Read(); r.State != ClassUnknown {
		t.Fatalf("Ready Unknown: %+v", r)
	}
}

// Codex's implementation review, finding 1: the reading is chosen from
// current evidence over the whole object. A condition reported for an older
// generation never hides a current failure and never holds a current Ready.
func TestClassReadingIsCurrentEvidence(t *testing.T) {
	cases := []struct {
		name  string
		class *NodeClass
		state string
		text  string
		note  string
	}{
		// AWS's controller runs every reconciler even when one errors: an AMI
		// lookup error leaves AMIsReady as it was while SubnetsReady is new
		{"an old first dependent beside a current one", ec2("x", 2, c("Ready", "False", "UnhealthyDependents", "", 2),
			c("AMIsReady", "False", "AMINotFound", "historical", 1), c("SubnetsReady", "False", "SubnetsNotFound", "current", 2)),
			ClassNotReady, "SubnetsReady False: SubnetsNotFound: no subnets matched the selector terms",
			"AMIsReady False, reported for generation 1 (the NodeClass is at 2): AMINotFound: no AMI matched the selector terms, or it is not usable"},
		// CapacityReservationsReady is a Ready dependent only with its feature
		// gate, and the provider skips its reconciler when the gate is off
		{"an old optional dependent beside a current Ready True", ec2("x", 2, c("Ready", "True", "", "", 2),
			c("CapacityReservationsReady", "False", "CapacityUnavailable", "old", 1)),
			ClassReady, "", "CapacityReservationsReady False, reported for generation 1 (the NodeClass is at 2): CapacityUnavailable: see the condition in Klyx"},
		// a current dependent's failure is current while the aggregate lags,
		// is absent or Unknown
		{"a current dependent beside an old Ready", ec2("x", 3, c("Ready", "True", "", "", 2), c("SubnetsReady", "False", "SubnetsNotFound", "", 3)),
			ClassNotReady, "SubnetsReady False: SubnetsNotFound: no subnets matched the selector terms", ""},
		{"a current dependent without Ready", ec2("x", 3, c("SecurityGroupsReady", "False", "SecurityGroupsNotFound", "", 3)),
			ClassNotReady, "SecurityGroupsReady False: SecurityGroupsNotFound: no security groups matched the selector terms", ""},
		{"a current dependent beside Ready Unknown", ec2("x", 3, c("Ready", "Unknown", "AwaitingReconciliation", "", 3), c("SubnetsReady", "False", "SubnetsNotFound", "", 3)),
			ClassNotReady, "SubnetsReady False: SubnetsNotFound: no subnets matched the selector terms", ""},
		// an unknown type decides only beside a current Ready False
		{"a current unknown type beside an old Ready", ec2("x", 3, c("Ready", "True", "", "", 2), c("VendorThing", "False", "Nope", "", 3)),
			ClassSyncing, "its Ready condition is behind generation 3", ""},
		{"a current unknown type beside a current Ready False", ec2("x", 3, c("Ready", "False", "UnhealthyDependents", "", 3), c("VendorThing", "False", "Nope", "", 3)),
			ClassNotReady, "VendorThing False: Nope: see the condition in Klyx", ""},
		// two old failures: the first in the provider's order is the note
		{"two old dependents", ec2("x", 3, c("Ready", "False", "UnhealthyDependents", "", 3), c("SubnetsReady", "False", "SubnetsNotFound", "", 2), c("AMIsReady", "False", "AMINotFound", "", 1)),
			ClassNotReady, "Ready False: UnhealthyDependents: a condition it depends on is not ready",
			"AMIsReady False, reported for generation 1 (the NodeClass is at 3): AMINotFound: no AMI matched the selector terms, or it is not usable"},
		// only old evidence: syncing, the old failure as a note
		{"everything old", ec2("x", 3, c("Ready", "False", "UnhealthyDependents", "", 2), c("SubnetsReady", "False", "SubnetsNotFound", "", 2)),
			ClassSyncing, "its Ready condition is behind generation 3",
			"SubnetsReady False, reported for generation 2 (the NodeClass is at 3): SubnetsNotFound: no subnets matched the selector terms"},
	}
	for _, tc := range cases {
		r := tc.class.Read()
		if r.State != tc.state || r.Text != tc.text || r.Note != tc.note {
			t.Errorf("%s: got %q %q note %q, want %q %q note %q", tc.name, r.State, r.Text, r.Note, tc.state, tc.text, tc.note)
		}
	}
	// the historical condition keeps no message: its note is Klyx's wording
	if h := cases[0].class.Historical; h == nil || h.Message != "" || h.Type != "AMIsReady" || h.ObservedGeneration != 1 {
		t.Fatalf("historical: %+v", h)
	}
	if cl := cases[0].class; cl.Failing == nil || cl.Failing.Type != "SubnetsReady" || cl.Failing.Message != "current" {
		t.Fatalf("the current failure keeps its message for the local inspector: %+v", cl.Failing)
	}
	// the pool reads the class's current failure, once (Codex's probe)
	p := ParseNodePool(poolU(2, ec2Ref, 2, c("Ready", "False", "UnhealthyDependents", "", 2), c("NodeClassReady", "False", "UnhealthyDependents", "", 2)))
	if v := PoolRule(p, found(cases[0].class)); v.Eval.Role != health.Fault || !strings.Contains(v.Eval.Reason, "SubnetsNotFound") || strings.Contains(v.Eval.Reason, "AMINotFound") {
		t.Fatalf("the pool: %+v", v.Eval)
	}
	if v := PoolRule(p, found(cases[1].class)); v.Eval.Role == health.Fault {
		t.Fatalf("a class ready now is not a fault from an old optional dependent: %+v (%s)", v.Eval, roles(v))
	}
}

// §9: a decisive ninth condition and unknown types beyond the display cap.
func TestClassCutKeepsTheAuthoritativeReading(t *testing.T) {
	var conds []map[string]any
	for i := 0; i < 12; i++ {
		conds = append(conds, c(fmt.Sprintf("Extra%02d", i), "True", "", "", 1))
	}
	conds = append(conds, c("SubnetsReady", "False", "SubnetsNotFound", "", 1), c("Ready", "False", "UnhealthyDependents", "", 1))
	cl := ec2("big", 1, conds...)
	if !cl.Conds.Cut || cl.Conds.Total != 14 || len(cl.Conds.Display) != maxConds {
		t.Fatalf("display cut: %+v", cl.Conds)
	}
	if r := cl.Read(); r.State != ClassNotReady || !strings.HasPrefix(r.Text, "SubnetsReady False") {
		t.Fatalf("the decisive conditions past the cap still decide: %+v", r)
	}
	if cl.UnknownTotal != 12 || len(cl.Unknown) != maxUnknown {
		t.Fatalf("unknown types are counted, never dropped: %d %v", cl.UnknownTotal, cl.Unknown)
	}
}

func TestClassFactsKeepNoIdentifiers(t *testing.T) {
	e := ParseNodeClass(obj("karpenter.k8s.aws/v1", "EC2NodeClass", "default", 1,
		map[string]any{"role": "KarpenterNodeRole", "subnetSelectorTerms": []any{map[string]any{"id": needleSubnet}}},
		map[string]any{
			"subnets":         []any{map[string]any{"id": needleSubnet, "zone": "eu-west-1a"}, map[string]any{"id": "subnet-2", "zone": "eu-west-1b"}},
			"securityGroups":  []any{map[string]any{"id": needleSG}},
			"amis":            []any{map[string]any{"id": needleAMI, "deprecated": true}},
			"instanceProfile": needleProfile,
		}, c("Ready", "True", "", "", 1)), AdapterEC2)
	f := e.Facts
	if *f.Subnets != 2 || *f.SecurityGroups != 1 || *f.AMIs != 1 || !f.DeprecatedAMI || f.Role != "KarpenterNodeRole" || f.InstanceProfile != needleProfile {
		t.Fatalf("ec2 facts: %+v", f)
	}
	a := ParseNodeClass(obj("karpenter.azure.com/v1beta1", "AKSNodeClass", "default", 1,
		map[string]any{"vnetSubnetID": needleARM, "imageFamily": "AzureLinux"},
		map[string]any{"kubernetesVersion": "1.33.2", "images": []any{map[string]any{"id": needleARM}}},
		c("Ready", "True", "", "", 1)), AdapterAKS)
	if !a.Facts.SubnetSet || a.Facts.KubernetesVersion != "1.33.2" || *a.Facts.Images != 1 || a.Facts.ImageFamily != "AzureLinux" {
		t.Fatalf("aks facts: %+v", a.Facts)
	}
	m := ParseNodeClass(obj("eks.amazonaws.com/v1", "NodeClass", "default", 1,
		map[string]any{"role": "AutoNodeRole", "podSubnetSelectorTerms": []any{map[string]any{"id": needleSubnet}}},
		map[string]any{"subnets": []any{map[string]any{"id": needleSubnet}}}, c("Ready", "True", "", "", 1)), AdapterEKSAuto)
	if !m.Facts.PodSubnets || *m.Facts.Subnets != 1 || m.Facts.SecurityGroups != nil || m.Facts.Role != "AutoNodeRole" {
		t.Fatalf("auto facts (nil count: not reported): %+v", m.Facts)
	}
	// No structured field keeps an ID; only the local-only profile name.
	for _, cl := range []NodeClass{e, a, m} {
		b, _ := json.Marshal(cl)
		for _, n := range []string{needleARM, needleSubnet, needleSG, needleAMI} {
			if strings.Contains(string(b), n) {
				t.Errorf("%s keeps %s: %s", cl.Adapter, n, b)
			}
		}
	}
}

// ---- the link (§3) -------------------------------------------------------

func TestResolveKeepsEveryLinkStateDistinct(t *testing.T) {
	cls := ec2("default", 1, c("Ready", "True", "", "", 1))
	ref := &ClassRef{Group: "karpenter.k8s.aws", Kind: "EC2NodeClass", Name: "default"}
	other := &ClassRef{Group: "karpenter.k8s.aws", Kind: "EC2NodeClass", Name: "absent"}
	asOf := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	ev := func(e KindEvidence) map[Adapter]KindEvidence { return map[Adapter]KindEvidence{AdapterEC2: e} }
	live := map[string]*NodeClass{"default": cls}
	cases := []struct {
		name  string
		ref   *ClassRef
		kinds map[Adapter]KindEvidence
		want  LinkState
	}{
		{"missing ref", nil, ev(KindEvidence{}), LinkMissingRef},
		{"empty name", &ClassRef{Group: "karpenter.k8s.aws", Kind: "EC2NodeClass"}, ev(KindEvidence{}), LinkMissingRef},
		{"unsupported provider", &ClassRef{Group: "karpenter.k8s.gcp", Kind: "GCENodeClass", Name: "x"}, ev(KindEvidence{}), LinkUnsupported},
		{"a wrong Auto Mode reference", &ClassRef{Group: "eks.amazonaws.com", Kind: "EC2NodeClass", Name: "default"}, ev(KindEvidence{}), LinkUnsupported},
		{"no evidence supplied", ref, nil, LinkUnresolved},
		{"kind not served", ref, ev(KindEvidence{}), LinkKindNotServed},
		{"another version only", ref, ev(KindEvidence{OtherVersion: true}), LinkVersionUnsupported},
		{"found", ref, ev(KindEvidence{Served: true, Current: true, Complete: true, Classes: live}), LinkFound},
		{"found in a capped kind", ref, ev(KindEvidence{Served: true, Current: true, Classes: live}), LinkFound},
		{"not found", other, ev(KindEvidence{Served: true, Current: true, Complete: true, Classes: live}), LinkNotFound},
		{"omitted by the kind budget", other, ev(KindEvidence{Served: true, Current: true, Classes: live}), LinkCut},
		{"stale", ref, ev(KindEvidence{Served: true, AsOf: asOf, Classes: live}), LinkStale},
		{"unreadable", ref, ev(KindEvidence{Served: true}), LinkUnreadable},
	}
	for _, tc := range cases {
		if l := Resolve(tc.ref, tc.kinds); l.State != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, l.State, tc.want)
		}
	}
	if l := Resolve(ref, ev(KindEvidence{Served: true, AsOf: asOf, Classes: live})); l.Class != cls || !l.AsOf.Equal(asOf) {
		t.Errorf("stale keeps its last good class and time: %+v", l)
	}
}

// ---- the NodePool rule (§3) ---------------------------------------------

func TestPoolTheClassWordsReplaceTheCopiedCondition(t *testing.T) {
	cls := ec2("default", 4,
		c("ValidationSucceeded", "False", "InstanceProfileNotFound", `Instance profile "`+needleProfile+`" not found`, 4),
		c("Ready", "False", "UnhealthyDependents", "", 4))
	// Core copies the class's reason and message into NodeClassReady and
	// derives Ready from it: one incident.
	p := ParseNodePool(poolU(2, ec2Ref, 4,
		c("NodeClassReady", "False", "InstanceProfileNotFound", `Instance profile "`+needleProfile+`" not found`, 2),
		c("ValidationSucceeded", "True", "", "", 2),
		c("Ready", "False", "UnhealthyDependents", "NodeClassReady=False", 2)))
	v := PoolRule(p, found(cls))
	if got := roles(v); got != "fault NodeClass default not ready" {
		t.Fatalf("one finding per pool: %s", got)
	}
	if v.Eval.Role != health.Fault || !v.Eval.Included ||
		v.Eval.Reason != "NodeClass default not ready: ValidationSucceeded False: InstanceProfileNotFound: the instance profile was not found" {
		t.Fatalf("the headline in Klyx's wording: %+v", v.Eval)
	}
	if !strings.Contains(v.Findings[0].Native, needleProfile) {
		t.Fatalf("the local inspector keeps the native text: %+v", v.Findings[0])
	}
}

// §3: on a class's recovery neither the aggregate nor the copy reintroduces
// the fault.
func TestPoolRecoveredClassWithALaggingPool(t *testing.T) {
	cls := ec2("default", 5, c("Ready", "True", "", "", 5))
	p := ParseNodePool(poolU(2, ec2Ref, 4,
		c("NodeClassReady", "False", "SubnetsNotFound", "", 2),
		c("ValidationSucceeded", "True", "", "", 2),
		c("Ready", "False", "UnhealthyDependents", "", 2)))
	v := PoolRule(p, found(cls))
	if got := roles(v); got != "progressing NodeClass default is ready; the NodePool's status has not caught up" || v.Eval.Included {
		t.Fatalf("recovered class: %s %+v", got, v.Eval)
	}
}

func TestPoolClassStates(t *testing.T) {
	p := ParseNodePool(poolU(1, ec2Ref, 0, c("NodeClassReady", "False", "NodeClassReadinessUnknown", "", 1),
		c("ValidationSucceeded", "True", "", "", 1), c("Ready", "False", "UnhealthyDependents", "", 1)))
	terminating := ec2("default", 1, c("Ready", "True", "", "", 1))
	terminating.Meta.Deleting = true
	syncing := ec2("default", 3, c("Ready", "False", "UnhealthyDependents", "", 2), c("SubnetsReady", "False", "SubnetsNotFound", "", 2))
	unknown := ec2("default", 1, c("Ready", "Unknown", "", "", 1))
	cases := []struct {
		name    string
		class   *NodeClass
		handler Handler
		want    string
	}{
		{"terminating", terminating, Handler{}, "fault NodeClass default is terminating"},
		{"syncing, never its old Ready", syncing, Handler{}, "progressing NodeClass default syncing"},
		{"Ready Unknown, a ready attributed controller", unknown, Handler{State: HandlerReady}, "progressing NodeClass default not yet reconciled"},
		{"Ready Unknown, platform managed", unknown, Handler{State: HandlerManaged}, "unknown NodeClass default not reconciled"},
		{"Ready Unknown, the controller not ready", unknown, Handler{State: HandlerNotReady}, "unknown NodeClass default not reconciled"},
		{"Ready Unknown, no handler", unknown, Handler{}, "unknown NodeClass default not reconciled"},
		{"no conditions, a ready controller", ec2("default", 1), Handler{State: HandlerReady}, "progressing NodeClass default not yet reconciled"},
	}
	for _, tc := range cases {
		in := found(tc.class)
		in.Handler = tc.handler
		if got := roles(PoolRule(p, in)); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
	managed := found(unknown)
	managed.Handler = Handler{State: HandlerManaged}
	if f := PoolRule(p, managed).Findings[0]; !strings.Contains(f.Text, "controller health is not observed") {
		t.Errorf("managed is never a ready controller: %+v", f)
	}
}

func TestPoolNotFoundAndMissingRef(t *testing.T) {
	p := ParseNodePool(poolU(1, ec2Ref, 0, c("NodeClassReady", "False", "NodeClassNotFound", "", 1),
		c("ValidationSucceeded", "True", "", "", 1), c("Ready", "False", "UnhealthyDependents", "", 1)))
	k, _ := KindFor(AdapterEC2)
	if got := roles(PoolRule(p, PoolInputs{Link: Link{State: LinkNotFound, Kind: k}})); got != "fault NodeClass EC2NodeClass/default not found" {
		t.Fatalf("not found: %s", got)
	}
	none := ParseNodePool(poolU(1, nil, 0, c("Ready", "True", "", "", 1)))
	if got := roles(PoolRule(none, PoolInputs{Link: Resolve(none.Ref, nil)})); got != "fault no nodeClassRef" {
		t.Fatalf("missing ref: %s", got)
	}
}

// §3 rule 4: a link not established is said beside the pool's own
// conditions, and NodeClassReady is read as reported - in Klyx's wording,
// never the inherited text (§8).
func TestPoolLinkNotEstablishedReadsTheReportedCopy(t *testing.T) {
	p := ParseNodePool(poolU(1, ec2Ref, 0,
		c("NodeClassReady", "False", "SubnetIDInvalid", "subnet "+needleARM+" is invalid", 1),
		c("ValidationSucceeded", "True", "", "", 1),
		c("Ready", "False", "UnhealthyDependents", "", 1)))
	k, _ := KindFor(AdapterEC2)
	for _, l := range []Link{
		{State: LinkKindNotServed, Kind: k}, {State: LinkVersionUnsupported, Kind: k}, {State: LinkUnreadable, Kind: k},
		{State: LinkStale, Kind: k, AsOf: time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)}, {State: LinkCut, Kind: k},
		{State: LinkUnsupported}, {State: LinkUnresolved, Kind: k},
	} {
		v := PoolRule(p, PoolInputs{Link: l})
		if len(v.Findings) != 2 || !v.Findings[0].Note || v.Findings[1].Label != "NodeClassReady False, as reported" || v.Eval.Role != health.Fault {
			t.Errorf("%s: %s", l.State, roles(v))
		}
		if strings.Contains(v.Eval.Reason, needleARM) || strings.Contains(v.Findings[1].Text, needleARM) || !strings.Contains(v.Findings[1].Native, needleARM) {
			t.Errorf("%s: the inherited text stays local: %+v", l.State, v.Findings[1])
		}
	}
	// The pool's own reading stands: healthy conditions are OK, with the note.
	ok := ParseNodePool(poolU(1, ec2Ref, 0, c("NodeClassReady", "True", "", "", 1), c("Ready", "True", "", "", 1)))
	if v := PoolRule(ok, PoolInputs{Link: Link{State: LinkUnreadable, Kind: k}}); v.Eval.Role != health.OK || roles(v) != "note class link not established" {
		t.Errorf("own conditions healthy: %s %+v", roles(v), v.Eval)
	}
}

func TestPoolNotReported(t *testing.T) {
	p := ParseNodePool(poolU(1, map[string]any{"group": "karpenter.azure.com", "kind": "AKSNodeClass", "name": "default"}, 0))
	k, _ := KindFor(AdapterAKS)
	in := PoolInputs{Link: Link{State: LinkKindNotServed, Kind: k}}
	v := PoolRule(p, in)
	if v.Eval.Role != health.Unknown || v.Eval.Label != "not reported" || strings.Contains(v.Findings[0].Text, "no running Karpenter") {
		t.Fatalf("not reported, the reference alone: %+v %+v", v.Eval, v.Findings)
	}
	in.Handler.NoneSupports = true
	if f := PoolRule(p, in).Findings[0]; f.Text != "no running Karpenter supports karpenter.azure.com/AKSNodeClass, so nothing reconciles this pool" {
		t.Fatalf("a complete, attributed inventory: %+v", f)
	}
}

func TestPoolOwnConditionsAndFreshness(t *testing.T) {
	cls := ec2("default", 4, c("Ready", "True", "", "", 4))
	in := found(cls)
	// ValidationSucceeded False: a fault; behind the generation: syncing.
	v := PoolRule(ParseNodePool(poolU(2, ec2Ref, 4, c("ValidationSucceeded", "False", "NodePoolValidationFailed", "x", 2),
		c("NodeClassReady", "True", "", "", 2), c("Ready", "False", "UnhealthyDependents", "", 2))), in)
	if roles(v) != "fault validation failed" {
		t.Errorf("validation: %s", roles(v))
	}
	v = PoolRule(ParseNodePool(poolU(3, ec2Ref, 4, c("ValidationSucceeded", "False", "NodePoolValidationFailed", "x", 2),
		c("NodeClassReady", "True", "", "", 3), c("Ready", "False", "UnhealthyDependents", "", 2))), in)
	if roles(v) != "progressing ValidationSucceeded syncing" {
		t.Errorf("an old validation failure is syncing: %s", roles(v))
	}
	// Ready False for another reason: a fault with Ready's reason.
	v = PoolRule(ParseNodePool(poolU(2, ec2Ref, 4, c("ValidationSucceeded", "True", "", "", 2),
		c("NodeClassReady", "True", "", "", 2), c("Ready", "False", "Odd", "", 2))), in)
	if roles(v) != "fault not ready" {
		t.Errorf("ready false: %s", roles(v))
	}
	// Ready True behind the generation is never confirmed.
	v = PoolRule(ParseNodePool(poolU(3, ec2Ref, 4, c("Ready", "True", "", "", 2))), in)
	if roles(v) != "progressing Ready syncing" {
		t.Errorf("old ready: %s", roles(v))
	}
	// Every finding healthy: OK, with Ready's evidence.
	v = PoolRule(ParseNodePool(poolU(2, ec2Ref, 4, c("ValidationSucceeded", "True", "", "", 2),
		c("NodeClassReady", "True", "", "", 2), c("Ready", "True", "", "", 2))), in)
	if v.Eval.Role != health.OK || v.Eval.Label != "ready" || v.Eval.Cond == nil || v.Eval.Reason != "" || len(v.Findings) != 0 {
		t.Errorf("healthy: %+v %s", v.Eval, roles(v))
	}
	// Conditions without Ready: unknown, never OK.
	v = PoolRule(ParseNodePool(poolU(2, ec2Ref, 4, c("ValidationSucceeded", "True", "", "", 2))), in)
	if v.Eval.Role != health.Unknown {
		t.Errorf("no Ready: %+v", v.Eval)
	}
	// An unreported generation is read as reported, and flagged.
	v = PoolRule(ParseNodePool(poolU(5, ec2Ref, 4, c("ValidationSucceeded", "False", "NodePoolValidationFailed", "", 0))), in)
	if roles(v) != "fault validation failed; unknown unknown" || !v.Findings[0].Unversioned {
		t.Errorf("unversioned: %s %+v", roles(v), v.Findings)
	}
}

// §3: NodeRegistrationHealthy False needs both fences.
func TestPoolRegistrationFences(t *testing.T) {
	reg := func(og int64) map[string]any {
		return c("NodeRegistrationHealthy", "False", "RegistrationFailed", "Failed to register node", og)
	}
	healthy := []map[string]any{c("ValidationSucceeded", "True", "", "", 2), c("NodeClassReady", "True", "", "", 2), c("Ready", "True", "", "", 2)}
	cls := ec2("default", 4, c("Ready", "True", "", "", 4))
	k, _ := KindFor(AdapterEC2)
	cases := []struct {
		name string
		gen  int64
		og   int64
		ncog int64
		link Link
		want string
	}{
		{"current", 2, 2, 4, Link{State: LinkFound, Kind: k, Class: cls}, "degraded nodes fail to register"},
		{"the pool changed", 3, 2, 4, Link{State: LinkFound, Kind: k, Class: cls}, "note registration failure is historical"},
		{"the class changed", 2, 2, 3, Link{State: LinkFound, Kind: k, Class: cls}, "note registration failure is historical"},
		{"the pool changed, the class unreadable", 3, 2, 4, Link{State: LinkUnreadable, Kind: k}, "note class link not established; note registration failure is historical"},
		{"pool generation unreported", 2, 0, 4, Link{State: LinkFound, Kind: k, Class: cls}, "unknown registration health not established"},
		{"class generation unreported", 2, 2, 0, Link{State: LinkFound, Kind: k, Class: cls}, "unknown registration health not established"},
		{"the class unreadable", 2, 2, 4, Link{State: LinkUnreadable, Kind: k}, "note class link not established; unknown registration health not established"},
		{"the class not found", 2, 2, 4, Link{State: LinkNotFound, Kind: k}, "fault NodeClass EC2NodeClass/default not found; unknown registration health not established"},
		{"unsupported provider", 2, 2, 4, Link{State: LinkUnsupported}, "note provider not supported; unknown registration health not established"},
	}
	for _, tc := range cases {
		p := ParseNodePool(poolU(tc.gen, ec2Ref, tc.ncog, append([]map[string]any{reg(tc.og)}, healthy...)...))
		// Keep the other conditions current for the pool's generation.
		for _, x := range []*Cond{p.CondValidation, p.CondNodeClassReady, p.CondReady} {
			x.ObservedGeneration = tc.gen
		}
		if got := roles(PoolRule(p, PoolInputs{Link: tc.link})); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
	// Mixed ages: a stale registration failure never hides a current
	// validation failure, and the reverse.
	p := ParseNodePool(poolU(3, ec2Ref, 4, reg(2), c("ValidationSucceeded", "False", "NodePoolValidationFailed", "", 3),
		c("NodeClassReady", "True", "", "", 3), c("Ready", "False", "UnhealthyDependents", "", 3)))
	if got := roles(PoolRule(p, found(cls))); got != "fault validation failed; note registration failure is historical" {
		t.Errorf("mixed ages: %s", got)
	}
	p = ParseNodePool(poolU(3, ec2Ref, 4, reg(3), c("ValidationSucceeded", "False", "NodePoolValidationFailed", "", 2),
		c("NodeClassReady", "True", "", "", 3), c("Ready", "False", "UnhealthyDependents", "", 3)))
	if got := roles(PoolRule(p, found(cls))); got != "progressing ValidationSucceeded syncing; degraded nodes fail to register" {
		t.Errorf("mixed ages, reversed: %s", got)
	}
}

// §7's cut contract: a decisive ninth condition decides.
func TestPoolDecisiveNinthCondition(t *testing.T) {
	var conds []map[string]any
	for i := 0; i < 9; i++ {
		conds = append(conds, c(fmt.Sprintf("Extra%02d", i), "True", "", "", 1))
	}
	conds = append(conds, c("ValidationSucceeded", "False", "NodePoolValidationFailed", "", 1), c("Ready", "False", "UnhealthyDependents", "", 1))
	p := ParseNodePool(poolU(1, ec2Ref, 1, conds...))
	if !p.Conds.Cut || p.Conds.Total != 11 {
		t.Fatalf("cut: %+v", p.Conds)
	}
	if got := roles(PoolRule(p, found(ec2("default", 1, c("Ready", "True", "", "", 1))))); got != "fault validation failed" {
		t.Fatalf("the conditions past the cap still decide: %s", got)
	}
}

// §9: the projection equals the raw read for every field a rule uses.
func TestPoolProjectionEqualsTheRawRead(t *testing.T) {
	u := poolU(7, ec2Ref, 6, c("Ready", "False", "UnhealthyDependents", "m1", 7), c("ValidationSucceeded", "True", "", "", 7),
		c("NodeClassReady", "False", "SubnetsNotFound", "m2", 6), c("NodeRegistrationHealthy", "False", "RegistrationFailed", "m3", 5))
	p := ParseNodePool(u)
	if p.Meta.Generation != 7 || p.NodeClassObservedGeneration != 6 || *p.Ref != (ClassRef{Group: "karpenter.k8s.aws", Kind: "EC2NodeClass", Name: "default"}) || p.Meta.UID != "uid-workers" {
		t.Fatalf("identity and link: %+v", p)
	}
	raw, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for i, want := range raw {
		m := want.(map[string]any)
		got := []*Cond{p.CondReady, p.CondValidation, p.CondNodeClassReady, p.CondRegistration}[i]
		if got.Type != m["type"] || got.Status != m["status"] || got.Reason != m["reason"] || got.Message != m["message"] ||
			got.ObservedGeneration != m["observedGeneration"].(int64) || got.At.IsZero() {
			t.Errorf("condition %d: %+v vs %v", i, got, m)
		}
	}
	if p.Ready != "not-ready" || p.Reason != "UnhealthyDependents" || p.Message != "m1" || p.ReadySince.IsZero() {
		t.Errorf("the lens fields: %+v", p)
	}
}

// §8: no AI-bound field of any verdict carries a needle, wherever it was
// planted; the local native text keeps it.
func TestPoolVerdictsCarryNoProviderText(t *testing.T) {
	msg := "subnet " + needleARM + " " + needleARN + " " + needleSubnet + " " + needleSG + " " + needleAMI + ` Instance profile "` + needleProfile + `" not found`
	k, _ := KindFor(AdapterEC2)
	cls := ec2("default", 1, c("SubnetsReady", "False", "SubnetsNotFound", msg, 1), c("Ready", "False", "UnhealthyDependents", msg, 1))
	pool := ParseNodePool(poolU(1, ec2Ref, 1, c("NodeClassReady", "False", "SubnetsNotFound", msg, 1),
		c("ValidationSucceeded", "False", "InstanceProfileNotFound", msg, 1), c("Ready", "False", "UnhealthyDependents", msg, 1),
		c("NodeRegistrationHealthy", "False", "LaunchFailed", msg, 1)))
	links := []Link{{State: LinkFound, Kind: k, Class: cls}, {State: LinkUnreadable, Kind: k}, {State: LinkStale, Kind: k, Class: cls}, {State: LinkUnsupported}}
	for _, l := range links {
		v := PoolRule(pool, PoolInputs{Link: l})
		for _, f := range v.Findings {
			for _, n := range needles {
				if strings.Contains(f.Label, n) || strings.Contains(f.Text, n) {
					t.Errorf("%s: finding %q carries %s", l.State, f.Label, n)
				}
			}
		}
		for _, n := range needles {
			if strings.Contains(v.Eval.Reason, n) || strings.Contains(v.Eval.Label, n) {
				t.Errorf("%s: the eval carries %s: %+v", l.State, n, v.Eval)
			}
		}
	}
	if r := cls.Read(); strings.Contains(r.Text, needleARM) || !strings.Contains(r.Native, needleARM) {
		t.Errorf("class reading: %+v", r)
	}
	claim := ParseNodeClaim(obj("karpenter.sh/v1", "NodeClaim", "c1", 1, nil, nil,
		c("Launched", "False", "LaunchFailed", msg, 1), c("Ready", "False", "UnhealthyDependents", msg, 1)), time.Now())
	e := ClaimRule(claim)
	for _, n := range needles {
		if strings.Contains(e.Reason, n) || strings.Contains(e.Label, n) {
			t.Errorf("claim eval carries %s: %+v", n, e)
		}
	}
	if !strings.Contains(claim.FailingMessage, needleProfile) {
		t.Errorf("the claim keeps its native message locally: %+v", claim)
	}
}

// §7: each MaxObject bounds its kind's cap product, measured.
func TestMaxObjectBoundsTheCapProduct(t *testing.T) {
	long := func(n int) string { return strings.Repeat("x", n) }
	labels := map[string]any{}
	for i := 0; i < 20; i++ {
		labels[fmt.Sprintf("%03d.%s", i, long(maxName-4))] = long(maxValue)
	}
	condsMax := func(types ...string) []map[string]any {
		var out []map[string]any
		// Every field past its cap: a type and status clip (with the
		// ellipsis), a reason at the longest token, a message clipped.
		for i := 0; i < 12; i++ {
			out = append(out, c(fmt.Sprintf("T%02d%s", i, long(400)), long(400), "R"+long(maxToken-1), long(4*maxMessage), 1))
		}
		// A class keeps its failing dependent only while it is False.
		for _, t := range types {
			status := long(400)
			if t != "Ready" && t != "NodeClassReady" && t != "ValidationSucceeded" && t != "NodeRegistrationHealthy" {
				status = "False"
			}
			out = append(out, c(t, status, "R"+long(maxToken-1), long(4*maxMessage), 1))
		}
		return out
	}
	big := func(u *unstructured.Unstructured) *unstructured.Unstructured {
		meta := u.Object["metadata"].(map[string]any)
		meta["name"], meta["uid"], meta["resourceVersion"] = long(400), long(100), long(200)
		meta["labels"] = labels
		return u
	}
	// a class keeps three authoritative conditions: Ready, its current
	// failure and an older one (generation 1 of 2), each past the cap
	classConds := condsMax("AMIsReady", "SubnetsReady", "Ready")
	for _, x := range classConds[12:] {
		if x["type"] != "AMIsReady" {
			x["observedGeneration"] = int64(2)
		}
	}
	class := ParseNodeClass(big(obj("karpenter.k8s.aws/v1", "EC2NodeClass", "x", 2,
		map[string]any{"role": long(400), "instanceProfile": long(400)},
		map[string]any{"instanceProfile": long(400), "kubernetesVersion": long(400)}, classConds...)), AdapterEC2)
	if class.Historical == nil || class.Failing == nil || class.Failing.Type != "SubnetsReady" {
		t.Fatalf("the cap product's class keeps its three conditions: %+v", class)
	}
	aks := ParseNodeClass(big(obj("karpenter.azure.com/v1beta1", "AKSNodeClass", "x", 1,
		map[string]any{"imageFamily": long(400)}, map[string]any{"kubernetesVersion": long(400)}, condsMax("ImagesReady", "Ready")...)), AdapterAKS)
	ref := map[string]any{"group": long(400), "kind": long(400), "name": long(400)}
	pool := ParseNodePool(big(poolU(1, ref, 1, condsMax("Ready", "ValidationSucceeded", "NodeClassReady", "NodeRegistrationHealthy")...)))
	claimU := big(obj("karpenter.sh/v1", "NodeClaim", "x", 1, map[string]any{"nodeClassRef": ref},
		map[string]any{"nodeName": long(400)}, condsMax("Launched", "Ready")...))
	claimU.Object["metadata"].(map[string]any)["labels"] = map[string]any{LabelNodePool: long(400), LabelCapacityType: long(400), LabelInstanceType: long(400)}
	claim := ParseNodeClaim(claimU, time.Now())
	t.Logf("the cap product: EC2NodeClass %d, AKSNodeClass %d, NodePool %d, NodeClaim %d bytes", class.size, aks.size, pool.size, claim.size)
	if class.size > MaxClassObject || aks.size > MaxClassObject || pool.size > MaxPoolObject || claim.size > MaxClaimObject {
		t.Fatalf("a cap product exceeds its MaxObject (%d, %d, %d)", MaxClassObject, MaxPoolObject, MaxClaimObject)
	}
	if !pool.Meta.LabelsCut || len(pool.Meta.Labels) > maxLabels || claim.Meta.Labels != nil {
		t.Fatalf("labels: %d cut=%v; claims keep none", len(pool.Meta.Labels), pool.Meta.LabelsCut)
	}
	if SizeOf(&pool) != pool.size || SizeOf(&claim) != claim.size || SizeOf(&class) != class.size || SizeOf("other") <= MaxClassObject {
		t.Fatal("SizeOf")
	}
	// A realistic object is far below its cap.
	small := ParseNodePool(poolU(1, ec2Ref, 1, c("Ready", "True", "", "", 1)))
	if small.size > 1<<10 {
		t.Fatalf("a small pool is %d bytes", small.size)
	}
}

func TestClaimAgesAtEvaluation(t *testing.T) {
	created := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	cl := ParseNodeClaim(obj("karpenter.sh/v1", "NodeClaim", "c1", 1, map[string]any{"nodeClassRef": ec2Ref}, nil), created)
	cl.Created = created
	if got := cl.At(created.Add(16 * time.Minute)); got.AgeSeconds != 960 {
		t.Fatalf("age: %d", got.AgeSeconds)
	}
	if cl.Ref == nil || cl.Ref.Kind != "EC2NodeClass" || cl.Ref.Name != "default" {
		t.Fatalf("the claim keeps its class reference: %+v", cl.Ref)
	}
}

func TestPoolFoundWithoutAClassIsUnresolved(t *testing.T) {
	p := ParseNodePool(poolU(1, ec2Ref, 1, c("NodeRegistrationHealthy", "False", "RegistrationFailed", "", 1), c("Ready", "True", "", "", 1)))
	k, _ := KindFor(AdapterEC2)
	if got := roles(PoolRule(p, PoolInputs{Link: Link{State: LinkFound, Kind: k}})); got != "note class link not read; unknown registration health not established" {
		t.Fatalf("%s", got)
	}
}

// Mutation survivors (S10 stage 6).
func TestClaimWithheldReason(t *testing.T) {
	cl := ParseNodeClaim(obj("karpenter.sh/v1", "NodeClaim", "c1", 1, nil, nil,
		c("Launched", "False", "launch failed for "+needleProfile, "m", 1), c("Ready", "False", "UnhealthyDependents", "", 1)), time.Now())
	if !cl.FailingReasonWithheld || cl.FailingReason != "" {
		t.Fatalf("a non-token launch reason is withheld: %+v", cl)
	}
	if e := ClaimRule(cl); e.Reason != "Launched False: reason withheld: "+Unworded {
		t.Fatalf("the claim's reason says so: %q", e.Reason)
	}
}

func TestClassFailingIsAFalseDependentOnly(t *testing.T) {
	cl := ec2("x", 1, c("AMIsReady", "True", "", "", 1), c("SubnetsReady", "Unknown", "AwaitingSubnets", "", 1),
		c("SecurityGroupsReady", "False", "SecurityGroupsNotFound", "", 1), c("Ready", "False", "UnhealthyDependents", "", 1))
	if cl.Failing == nil || cl.Failing.Type != "SecurityGroupsReady" {
		t.Fatalf("an Unknown dependent is not the failure: %+v", cl.Failing)
	}
}

func TestPoolNotesNeverHeadline(t *testing.T) {
	k, _ := KindFor(AdapterEC2)
	ok := ParseNodePool(poolU(1, ec2Ref, 0, c("NodeClassReady", "True", "", "", 1), c("Ready", "True", "", "", 1)))
	if v := PoolRule(ok, PoolInputs{Link: Link{State: LinkUnreadable, Kind: k}}); v.Eval.Label != "ready" || v.Eval.Reason != "" {
		t.Fatalf("a pool with only a note is ready: %+v", v.Eval)
	}
}
