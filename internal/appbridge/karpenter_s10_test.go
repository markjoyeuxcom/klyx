package appbridge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/crd"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/karpenter"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/workloads"
)

// Coverage S10 §8-§9, the identifier boundary through the bridge. The
// needles: an ARM ID, an ARN, AWS subnet, security group and AMI IDs, and a
// bare, non-ARN, unprefixed instance-profile name no pattern can recognise.
const (
	s10ARM     = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg-needle/providers/Microsoft.Network/virtualNetworks/v/subnets/s"
	s10ARN     = "arn:aws:iam::123456789012:role/NeedleRole"
	s10Subnet  = "subnet-0a1b2c3d4e5f60718"
	s10SG      = "sg-0a1b2c3d4e5f60718"
	s10AMI     = "ami-0a1b2c3d4e5f60718"
	s10Profile = "ExamplePrivateProfileNeedle"
)

var s10Needles = []string{s10ARM, "rg-needle", s10ARN, "NeedleRole", s10Subnet, s10SG, s10AMI, s10Profile}

// s10Msg is a provider message carrying every needle.
var s10Msg = "subnet " + s10ARM + " role " + s10ARN + " " + s10Subnet + " " + s10SG + " " + s10AMI + ` Instance profile "` + s10Profile + `" not found`

func noNeedle(t *testing.T, where, s string) {
	t.Helper()
	for _, n := range s10Needles {
		if strings.Contains(s, n) {
			t.Errorf("%s carries %q:\n%s", where, n, s)
		}
	}
}

func s10Cond(typ, status, reason string) map[string]any {
	return map[string]any{"type": typ, "status": status, "reason": reason, "message": s10Msg, "observedGeneration": int64(1),
		"lastTransitionTime": "2026-10-20T09:00:00Z"}
}

func s10U(apiVersion, kind, name string, spec, status map[string]any, conds ...map[string]any) *unstructured.Unstructured {
	cs := []any{}
	for _, c := range conds {
		cs = append(cs, c)
	}
	if status == nil {
		status = map[string]any{}
	}
	status["conditions"] = cs
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": apiVersion, "kind": kind,
		"metadata": map[string]any{"name": name, "uid": name + "-uid", "generation": int64(1), "creationTimestamp": "2026-10-20T08:00:00Z"},
		"spec":     spec, "status": status}}
}

func s10Class() *unstructured.Unstructured {
	return s10U("karpenter.k8s.aws/v1", "EC2NodeClass", "default",
		map[string]any{"role": s10ARN, "instanceProfile": s10Profile, "subnetSelectorTerms": []any{map[string]any{"id": s10Subnet}},
			"securityGroupSelectorTerms": []any{map[string]any{"id": s10SG}}, "amiSelectorTerms": []any{map[string]any{"id": s10AMI}}},
		map[string]any{"subnets": []any{map[string]any{"id": s10Subnet, "zone": "eu-west-1a"}}, "securityGroups": []any{map[string]any{"id": s10SG}},
			"amis": []any{map[string]any{"id": s10AMI, "name": "al2023"}}, "instanceProfile": s10Profile},
		s10Cond("InstanceProfileReady", "False", "InstanceProfileNotFound"), s10Cond("Ready", "False", "UnhealthyDependents"))
}

func s10Pool() *unstructured.Unstructured {
	return s10U("karpenter.sh/v1", "NodePool", "workers",
		map[string]any{"template": map[string]any{"spec": map[string]any{"nodeClassRef": map[string]any{"group": "karpenter.k8s.aws", "kind": "EC2NodeClass", "name": "default"},
			"requirements": []any{map[string]any{"key": "subnet", "operator": "In", "values": []any{s10Subnet}}}}}},
		nil, s10Cond("NodeClassReady", "False", "InstanceProfileNotFound"), s10Cond("ValidationSucceeded", "True", ""),
		s10Cond("Ready", "False", "UnhealthyDependents"))
}

func s10Claim() *unstructured.Unstructured {
	return s10U("karpenter.sh/v1", "NodeClaim", "workers-x1", map[string]any{"nodeClassRef": map[string]any{"group": "karpenter.k8s.aws", "kind": "EC2NodeClass", "name": "default"}},
		map[string]any{"providerID": "aws:///eu-west-1a/i-0a1b2c3d4e5f60718", "imageID": s10AMI},
		s10Cond("Launched", "False", "LaunchFailed"), s10Cond("Ready", "False", "UnhealthyDependents"))
}

func s10Rows(part string, set any) observe.Rows {
	return observe.Rows{Cluster: "c", Incarnation: 1, Task: healthsrc.TaskKarpenter, EvaluatedAt: evalAt,
		Part: observe.Part{ID: part, Classifier: "native", Coverage: observe.Live}, Rows: set}
}

func s10Detail(u *unstructured.Unstructured, related ...crd.RelatedRef) crd.InstanceDetail {
	d := crd.InstanceDetail{Kind: u.GetKind(), Name: u.GetName(), UID: string(u.GetUID()), Generation: 1,
		Conditions: []crd.Condition{{Type: "Ready", Status: "False", Reason: "UnhealthyDependents", Message: s10Msg},
			{Type: "Odd", Status: "False", Reason: "has " + s10Profile + " in it", Message: s10Msg}},
		Facts:   []crd.Fact{{Label: "role", Value: s10ARN}, {Label: "instance profile", Value: s10Profile}},
		Events:  []crd.Event{{Type: "Warning", Reason: "FailedLaunch", Message: s10Msg, Count: 3, Last: evalAt}},
		Related: related, YAML: "kind: " + u.GetKind()}
	return d
}

// the conn: a pool whose class kind is unreadable (the inherited text is
// all there is), a claim, the class as a fact row, and their details
func s10Conn() (*fakeExplainConn, healthsrc.Row[healthsrc.KarpenterPoolRow]) {
	ec2, _ := karpenter.KindFor(karpenter.AdapterEC2)
	pool := karpenter.ParseNodePool(s10Pool())
	link := karpenter.Link{State: karpenter.LinkUnreadable, Kind: ec2}
	v := karpenter.PoolRule(pool, karpenter.PoolInputs{Link: link})
	prow := healthsrc.Row[healthsrc.KarpenterPoolRow]{Ref: healthsrc.RowRef{UID: "workers-uid", GVR: "karpenter.sh/v1/nodepools", Name: "workers", Generation: 1},
		Eval: v.Eval, Data: healthsrc.KarpenterPoolRow{Pool: pool, Findings: v.Findings, Link: link}}
	claim := karpenter.ParseNodeClaim(s10Claim(), evalAt)
	crow := healthsrc.Row[karpenter.NodeClaim]{Ref: healthsrc.RowRef{UID: "workers-x1-uid", GVR: "karpenter.sh/v1/nodeclaims", Name: "workers-x1", Generation: 1},
		Eval: karpenter.ClaimRule(claim), Data: claim}
	class := karpenter.ParseNodeClass(s10Class(), karpenter.AdapterEC2)
	krow := healthsrc.Row[healthsrc.KarpenterClassRow]{Ref: healthsrc.RowRef{UID: "default-uid", GVR: "karpenter.k8s.aws/v1/ec2nodeclasses", Name: "default", Generation: 1},
		Data: healthsrc.KarpenterClassRow{Class: &class, Kind: ec2, Reading: class.Read(), Users: []string{"workers"}, UsersComplete: true}}
	classRef := crd.RelatedRef{Kind: "EC2NodeClass", Name: "default", Group: "karpenter.k8s.aws", Version: "v1", Plural: "ec2nodeclasses", Relation: "class"}
	return &fakeExplainConn{
		rows: map[string]observe.Rows{
			"karpenter/nodepools":   s10Rows("nodepools", healthsrc.RowSet[healthsrc.KarpenterPoolRow]{Rows: []healthsrc.Row[healthsrc.KarpenterPoolRow]{prow}}),
			"karpenter/nodeclaims":  s10Rows("nodeclaims", healthsrc.RowSet[karpenter.NodeClaim]{Rows: []healthsrc.Row[karpenter.NodeClaim]{crow}}),
			"karpenter/nodeclasses": s10Rows("nodeclasses", healthsrc.RowSet[healthsrc.KarpenterClassRow]{Rows: []healthsrc.Row[healthsrc.KarpenterClassRow]{krow}}),
		},
		details: map[string]crd.InstanceDetail{
			"karpenter.sh/nodepools//workers":           s10Detail(s10Pool(), classRef),
			"karpenter.sh/nodeclaims//workers-x1":       s10Detail(s10Claim(), classRef),
			"karpenter.k8s.aws/ec2nodeclasses//default": s10Detail(s10Class()),
		},
	}, prow
}

// §9: Explain's serialized document - the class's conditions, the pool's
// copied messages with its class unavailable, a claim's failing link, the
// spec through facts, events and related objects - carries no needle,
// while the local inspector DTO keeps the native text.
func TestS10ExplainCarriesNoProviderText(t *testing.T) {
	conn, prow := s10Conn()
	for _, target := range []ExplainTargetDTO{
		{Kind: "row", Task: healthsrc.TaskKarpenter, Part: "nodepools", UID: "workers-uid"},
		{Kind: "row", Task: healthsrc.TaskKarpenter, Part: "nodeclaims", UID: "workers-x1-uid"},
		{Kind: "row", Task: healthsrc.TaskKarpenter, Part: "nodeclasses", UID: "default-uid"},
		{Kind: "object", Group: "karpenter.k8s.aws", Version: "v1", Plural: "ec2nodeclasses", Name: "default", UID: "default-uid"},
	} {
		cl := &fakeCompleter{reply: "ok"}
		r := explainSvc(cl, conn).Explain("c", target)
		if !r.OK {
			t.Fatalf("%s/%s: %+v", target.Part, target.Plural, r)
		}
		noNeedle(t, "Explain "+target.Part+target.Plural, cl.gotUser)
		noNeedle(t, "Explain grounding "+target.Part+target.Plural, r.GroundedOn)
		ev := evidenceOf(t, cl.gotUser)
		tg := ev["target"].(map[string]any)
		if tg["facts"] != nil {
			t.Errorf("%s: the generic facts are not sent for a Karpenter kind: %v", target.Part, tg["facts"])
		}
		for _, c := range tg["conditions"].([]any) {
			m := c.(map[string]any)
			if m["type"] == "Odd" && m["reason"] != "reason withheld" {
				t.Errorf("a reason outside the pattern is withheld: %v", m)
			}
		}
		if target.Part == "nodeclasses" && ev["klyxVerdict"] != nil {
			t.Errorf("a class row is a fact: no verdict: %v", ev["klyxVerdict"])
		}
	}
	// The local inspector keeps the native text, the headline finding's own.
	dto := karpenterPoolRowDTO(prow)
	b, _ := json.Marshal(dto)
	if !strings.Contains(string(b), s10Profile) || len(dto.Findings) < 2 || !strings.Contains(dto.Findings[1].Native, s10Profile) {
		t.Fatalf("the pool DTO keeps the native text locally: %s", b)
	}
	b, _ = json.Marshal(karpenterClassRowDTO(conn.rows["karpenter/nodeclasses"].Rows.(healthsrc.RowSet[healthsrc.KarpenterClassRow]).Rows[0]))
	if !strings.Contains(string(b), s10Profile) || !strings.Contains(string(b), s10ARN) {
		t.Fatalf("the class DTO keeps the native text and the local facts: %s", b)
	}
}

// Suggest-fix: a class is refused before anything is read; a pool's
// manifest crosses without its status, its identifiers redacted.
func TestS10SuggestFix(t *testing.T) {
	conn, _ := s10Conn()
	cl := &fakeCompleter{reply: "ok"}
	s := explainSvc(cl, conn)
	if r := s.SuggestFix("c", "karpenter.k8s.aws", "v1", "ec2nodeclasses", "", "default", "default-uid"); r.Error == "" || cl.calls != 0 || conn.getCall != 0 {
		t.Fatalf("a class's Suggest-fix is refused unread: %+v (calls %d, gets %d)", r, cl.calls, conn.getCall)
	}
	raw, _ := json.Marshal(s10Pool().Object)
	d := conn.details["karpenter.sh/nodepools//workers"]
	d.YAML = string(raw) // JSON is YAML
	conn.details["karpenter.sh/nodepools//workers"] = d
	r := s.SuggestFix("c", "karpenter.sh", "v1", "nodepools", "", "workers", "workers-uid")
	if !r.OK {
		t.Fatalf("a pool's Suggest-fix: %+v", r)
	}
	noNeedle(t, "the Suggest-fix prompt", cl.gotSystem+cl.gotUser)
	if !strings.Contains(cl.gotUser, "<aws-id>") || strings.Contains(cl.gotUser, "UnhealthyDependents\n") {
		t.Errorf("the spec's identifier is redacted, the status dropped: %s", cl.gotUser)
	}
}

func s10Chat() *fakeChatConn {
	f := newFakeChat()
	add := func(kind string, gvr schema.GroupVersionResource, u *unstructured.Unstructured) {
		f.kinds[kind] = []fleet.ChatKind{{GVR: gvr, Kind: u.GetKind()}}
		f.kinds[gvr.Resource] = f.kinds[kind] // the snapshot reads by resource
		f.objects[key(gvr, "", u.GetName())] = u
	}
	add("ec2nodeclass", schema.GroupVersionResource{Group: "karpenter.k8s.aws", Version: "v1", Resource: "ec2nodeclasses"}, s10Class())
	add("nodepool", schema.GroupVersionResource{Group: "karpenter.sh", Version: "v1", Resource: "nodepools"}, s10Pool())
	add("nodeclaim", schema.GroupVersionResource{Group: "karpenter.sh", Version: "v1", Resource: "nodeclaims"}, s10Claim())
	aks := s10U("karpenter.azure.com/v1beta1", "AKSNodeClass", "azure", map[string]any{"vnetSubnetID": s10ARM}, nil, s10Cond("SubnetsReady", "False", "SubnetNotFound"))
	add("aksnodeclass", schema.GroupVersionResource{Group: "karpenter.azure.com", Version: "v1beta1", Resource: "aksnodeclasses"}, aks)
	f.events = []workloads.EventSummary{
		{Type: "Warning", Reason: "FailedLaunch", Message: s10Msg, Kind: "NodeClaim", Name: "workers-x1", Count: 2, LastSeenUnix: evalAt.Unix()},
		{Type: "Warning", Reason: "BackOff", Message: "pod on " + s10Subnet, Kind: "Pod", Namespace: "shop", Name: "api", LastSeenUnix: evalAt.Unix()},
	}
	return f
}

// Chat's tool results: get, events and list, for every Karpenter kind.
func TestS10ChatCarriesNoProviderText(t *testing.T) {
	f := s10Chat()
	tools := newChatTools(f, fixedNow)
	for _, kind := range []string{"ec2nodeclass", "nodepool", "nodeclaim", "aksnodeclass"} {
		name := map[string]string{"ec2nodeclass": "default", "nodepool": "workers", "nodeclaim": "workers-x1", "aksnodeclass": "azure"}[kind]
		res := tools.Run(context.Background(), toolGet, json.RawMessage(`{"kind":"`+kind+`","name":"`+name+`"}`))
		noNeedle(t, "chat get "+kind, string(res.JSON))
		if res.Read.Outcome != "completed" {
			t.Fatalf("get %s: %+v", kind, res.Read)
		}
		res = tools.Run(context.Background(), toolList, json.RawMessage(`{"kind":"`+kind+`"}`))
		noNeedle(t, "chat list "+kind, string(res.JSON))
	}
	res := tools.Run(context.Background(), toolEvents, json.RawMessage(`{"limit":20}`))
	noNeedle(t, "chat events", string(res.JSON))
	if !strings.Contains(string(res.JSON), "FailedLaunch") || !strings.Contains(string(res.JSON), `pod on \u003caws-id\u003e`) {
		t.Fatalf("a Karpenter event keeps its reason; another kind's text is redacted, not dropped: %s", res.JSON)
	}
	// The class's adapter facts cross; never its role or instance profile.
	res = tools.Run(context.Background(), toolGet, json.RawMessage(`{"kind":"ec2nodeclass","name":"default"}`))
	if !strings.Contains(string(res.JSON), `"subnets":1`) || !strings.Contains(string(res.JSON), "the instance profile was not found") {
		t.Fatalf("the class's counts and Klyx's wording: %s", res.JSON)
	}
}

// Codex's S10 design review (cd00d87): the generic chat projection kept an
// AKSNodeClass's subnet ARM ID from its spec and its condition; now a
// regression.
func TestReviewS10DesignGenericClassProjectionExclusions(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "karpenter.azure.com/v1beta1", "kind": "AKSNodeClass",
		"metadata": map[string]any{"name": "example"},
		"spec":     map[string]any{"vnetSubnetID": "/subscriptions/11111111-1111-1111-1111-111111111111/resourceGroups/S10SpecNeedle/providers/Microsoft.Network/virtualNetworks/v/subnets/s"},
		"status": map[string]any{"conditions": []any{map[string]any{"type": "SubnetsReady", "status": "False", "reason": "SubnetNotFound",
			"message": "resource not found: /subscriptions/11111111-1111-1111-1111-111111111111/resourceGroups/S10ConditionNeedle/providers/Microsoft.Network/virtualNetworks/v/subnets/s"}}},
	}}
	b, err := json.Marshal(chatProject(schema.GroupVersionResource{Group: "karpenter.azure.com", Version: "v1beta1", Resource: "aksnodeclasses"}, u))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "S10SpecNeedle") || strings.Contains(string(b), "S10ConditionNeedle") {
		t.Fatalf("the chat projection disclosed a class's ARM ID: %s", b)
	}
}

// Chat's attention summary and a no-tools snapshot: Klyx's own wording only.
func TestS10ChatAttentionAndSnapshot(t *testing.T) {
	conn, prow := s10Conn()
	_ = conn
	f := s10Chat()
	f.summary = &observe.Summary{EvaluatedAt: fixedNow(), Connection: "connected", Tasks: []observe.Task{{ID: healthsrc.TaskKarpenter,
		Parts: []observe.Part{{ID: "nodepools", Classifier: "native", Coverage: observe.Live,
			Top: []observe.Ref{{Name: "workers", UID: "workers-uid", GVR: "karpenter.sh/v1/nodepools", Role: prow.Eval.Role.String(), Label: prow.Eval.Label, Reason: prow.Eval.Reason}}}}}}}
	res := newChatTools(f, fixedNow).Run(context.Background(), toolAttention, json.RawMessage(`{}`))
	noNeedle(t, "chat attention", string(res.JSON))
	if !strings.Contains(string(res.JSON), "workers") {
		t.Fatalf("attention lists the pool: %s", res.JSON)
	}

	p := &fakeConverser{}
	fx := newChatFixture(t, snapshotOnly{p})
	fx.conn = f
	fx.cur = f
	st := fx.svc.Start("x")
	fx.svc.Ask("x", st.ConversationID, "why can ec2nodeclass/default not launch nodes for nodepool/workers", nil)
	if len(p.creqs) == 0 {
		t.Fatal("no snapshot was sent")
	}
	doc := p.creqs[0].User
	noNeedle(t, "the chat snapshot", doc)
	if !strings.Contains(doc, `"name":"default"`) || !strings.Contains(doc, `"name":"workers"`) {
		t.Fatalf("the snapshot read both objects: %s", doc)
	}
}

var _ = time.Second

// Codex's implementation review, finding 1: a False condition from an older
// generation travels as a note in Klyx's wording - to the lens, and to
// Explain through the allowlist - and never with its message.
func TestS10ReadingNoteIsKlyxWording(t *testing.T) {
	cond := func(typ, status, reason string, og int64) map[string]any {
		return map[string]any{"type": typ, "status": status, "reason": reason, "message": "for " + s10Profile, "observedGeneration": og, "lastTransitionTime": "2026-10-20T09:00:00Z"}
	}
	u := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "karpenter.k8s.aws/v1", "kind": "EC2NodeClass",
		"metadata": map[string]any{"name": "default", "uid": "default-uid", "generation": int64(2)},
		"status":   map[string]any{"conditions": []any{cond("AMIsReady", "False", "AMINotFound", 1), cond("Ready", "False", "UnhealthyDependents", 2)}}}}
	class := karpenter.ParseNodeClass(u, karpenter.AdapterEC2)
	ec2, _ := karpenter.KindFor(karpenter.AdapterEC2)
	want := "AMIsReady False, reported for generation 1 (the NodeClass is at 2): AMINotFound: no AMI matched the selector terms, or it is not usable"
	krow := healthsrc.Row[healthsrc.KarpenterClassRow]{Ref: healthsrc.RowRef{UID: "default-uid"}, Data: healthsrc.KarpenterClassRow{Class: &class, Kind: ec2, Reading: class.Read(), UsersComplete: true}}
	dto := karpenterClassRowDTO(krow)
	if dto.Reading.Note != want || dto.Reading.State != "not ready" {
		t.Fatalf("the class DTO's reading: %+v", dto.Reading)
	}
	r := class.Read()
	pool := karpenter.ParseNodePool(s10Pool())
	prow := healthsrc.Row[healthsrc.KarpenterPoolRow]{Data: healthsrc.KarpenterPoolRow{Pool: pool, Link: karpenter.Link{State: karpenter.LinkFound, Kind: ec2, Class: &class}, Class: &r}}
	pdto := karpenterPoolRowDTO(prow)
	if pdto.Class == nil || pdto.Class.Note != want {
		t.Fatalf("the pool DTO's class reading: %+v", pdto.Class)
	}
	project := func(key string, v any) string {
		b, _ := json.Marshal(v)
		var m any
		_ = json.Unmarshal(b, &m)
		out, _ := json.Marshal((&scrubber{}).project(m, aiEvidenceFields[key]))
		return string(out)
	}
	for key, v := range map[string]any{healthsrc.TaskKarpenter + "/nodeclasses": dto, healthsrc.TaskKarpenter + "/nodepools": pdto} {
		got := project(key, v)
		if !strings.Contains(got, `"note":"`+want+`"`) || strings.Contains(got, s10Profile) {
			t.Errorf("%s evidence carries the note in Klyx's words, never the message: %s", key, got)
		}
	}
}
