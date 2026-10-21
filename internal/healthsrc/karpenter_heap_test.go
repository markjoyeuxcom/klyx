package healthsrc

import (
	"fmt"
	"runtime"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/karpenter"
)

// Coverage S10 §7's measurement: each Karpenter kind filled to its budget
// with projections - a typical shape and one at every cap - and the heap
// they hold reported beside the encoded bytes the budget counts. Run with
//
//	go test ./internal/healthsrc -run TestKarpenterHeapCalibration -v
//
// and record the logged figures in the S10 note's As built.

func karpCond(t, status, reason, message string) map[string]any {
	return map[string]any{"type": t, "status": status, "reason": reason, "message": message, "observedGeneration": int64(2),
		"lastTransitionTime": "2026-10-20T10:00:00Z"}
}

func karpObject(apiVersion, kind string, i int, worst bool, spec, status map[string]any, conds []any) *unstructured.Unstructured {
	labels := map[string]any{"app.kubernetes.io/managed-by": "Helm", "kustomize.toolkit.fluxcd.io/name": "infra", "kustomize.toolkit.fluxcd.io/namespace": "flux-system"}
	name := fmt.Sprintf("class-%06d", i)
	if worst {
		for j := 0; j < 20; j++ {
			labels[fmt.Sprintf("team.example.com/label-%02d-%s", j, strings.Repeat("k", 200))] = strings.Repeat("v", 63)
		}
		name = fmt.Sprintf("%06d-%s", i, strings.Repeat("n", 300))
		conds = nil
		for j := 0; j < 14; j++ {
			conds = append(conds, karpCond(fmt.Sprintf("Cond%02d", j)+strings.Repeat("t", 300), strings.Repeat("s", 40), "R"+strings.Repeat("r", 127), strings.Repeat("m", 4096)))
		}
		for _, t := range []string{"Ready", "ValidationSucceeded", "NodeClassReady", "NodeRegistrationHealthy", "SubnetsReady", "Launched", "Registered", "Initialized"} {
			conds = append(conds, karpCond(t, strings.Repeat("s", 40), "R"+strings.Repeat("r", 127), strings.Repeat("m", 4096)))
		}
	}
	status["conditions"] = conds
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion, "kind": kind,
		"metadata": map[string]any{"name": name, "uid": fmt.Sprintf("uid-%06d", i), "generation": int64(2), "creationTimestamp": "2026-10-20T10:00:00Z",
			"resourceVersion": "123456", "labels": labels, "annotations": map[string]any{"kubectl.kubernetes.io/last-applied-configuration": strings.Repeat("x", 2048)}},
		"spec": spec, "status": status,
	}}
}

func karpClassObject(a karpenter.Adapter, i int, worst bool) *unstructured.Unstructured {
	k, _ := karpenter.KindFor(a)
	spec := map[string]any{"role": "KarpenterNodeRole-prod", "amiSelectorTerms": []any{map[string]any{"alias": "al2023@latest"}},
		"subnetSelectorTerms": []any{map[string]any{"tags": map[string]any{"karpenter.sh/discovery": "prod"}}}, "securityGroupSelectorTerms": []any{map[string]any{"tags": map[string]any{"karpenter.sh/discovery": "prod"}}}}
	status := map[string]any{"subnets": []any{map[string]any{"id": "subnet-0a1b2c3d4e5f60718", "zone": "eu-west-1a"}, map[string]any{"id": "subnet-0a1b2c3d4e5f60719", "zone": "eu-west-1b"}},
		"securityGroups": []any{map[string]any{"id": "sg-0a1b2c3d4e5f60718", "name": "nodes"}}, "amis": []any{map[string]any{"id": "ami-0a1b2c3d4e5f60718", "name": "al2023"}},
		"instanceProfile": "prod_profile_1234567890", "kubernetesVersion": "1.33.2", "images": []any{map[string]any{"id": "/CommunityGalleries/x/images/y/versions/z"}}}
	if worst {
		spec["role"], spec["instanceProfile"], spec["imageFamily"] = strings.Repeat("r", 512), strings.Repeat("p", 512), strings.Repeat("f", 512)
		status["instanceProfile"], status["kubernetesVersion"] = strings.Repeat("p", 512), strings.Repeat("v", 512)
	}
	var conds []any
	for _, t := range []string{"AMIsReady", "SubnetsReady", "SecurityGroupsReady", "InstanceProfileReady", "ValidationSucceeded", "Ready"} {
		conds = append(conds, karpCond(t, "True", t, ""))
	}
	return karpObject(k.Group+"/"+k.Version, k.Kind, i, worst, spec, status, conds)
}

func karpPoolObject(i int, worst bool) *unstructured.Unstructured {
	ref := map[string]any{"group": "karpenter.k8s.aws", "kind": "EC2NodeClass", "name": "default"}
	if worst {
		ref = map[string]any{"group": strings.Repeat("g", 400), "kind": strings.Repeat("K", 400), "name": strings.Repeat("n", 400)}
	}
	spec := map[string]any{"weight": int64(10), "limits": map[string]any{"cpu": "1000", "memory": "4000Gi"},
		"template":   map[string]any{"spec": map[string]any{"nodeClassRef": ref, "requirements": []any{map[string]any{"key": "karpenter.sh/capacity-type", "operator": "In", "values": []any{"spot", "on-demand"}}}}},
		"disruption": map[string]any{"consolidationPolicy": "WhenEmptyOrUnderutilized", "consolidateAfter": "1m"}}
	status := map[string]any{"resources": map[string]any{"cpu": "36", "memory": "64Gi", "nodes": "3", "pods": "120"}, "nodeClassObservedGeneration": int64(2)}
	var conds []any
	for _, t := range []string{"ValidationSucceeded", "NodeClassReady", "NodeRegistrationHealthy", "Ready"} {
		conds = append(conds, karpCond(t, "True", t, ""))
	}
	return karpObject("karpenter.sh/v1", "NodePool", i, worst, spec, status, conds)
}

func karpClaimObject(i int, worst bool) *unstructured.Unstructured {
	spec := map[string]any{"nodeClassRef": map[string]any{"group": "karpenter.k8s.aws", "kind": "EC2NodeClass", "name": "default"},
		"requirements": []any{map[string]any{"key": "node.kubernetes.io/instance-type", "operator": "In", "values": []any{"m7g.large", "m7g.xlarge", "c7g.large"}}}}
	status := map[string]any{"nodeName": fmt.Sprintf("ip-10-0-%d-%d.eu-west-1.compute.internal", i/250, i%250), "providerID": "aws:///eu-west-1a/i-0a1b2c3d4e5f60718",
		"imageID": "ami-0a1b2c3d4e5f60718", "capacity": map[string]any{"cpu": "2", "memory": "8Gi", "pods": "29"}}
	var conds []any
	for _, t := range []string{"Launched", "Registered", "Initialized", "Ready"} {
		conds = append(conds, karpCond(t, "True", t, ""))
	}
	u := karpObject("karpenter.sh/v1", "NodeClaim", i, worst, spec, status, conds)
	labels := map[string]string{karpenter.LabelNodePool: "default", karpenter.LabelCapacityType: "spot", karpenter.LabelInstanceType: "m7g.large",
		"topology.kubernetes.io/zone": "eu-west-1a", "kubernetes.io/arch": "arm64", "karpenter.k8s.aws/instance-category": "m"}
	if worst {
		labels[karpenter.LabelNodePool], labels[karpenter.LabelCapacityType], labels[karpenter.LabelInstanceType] = strings.Repeat("p", 400), strings.Repeat("c", 400), strings.Repeat("i", 400)
		status["nodeName"] = strings.Repeat("n", 400)
	}
	u.SetLabels(labels)
	return u
}

func TestKarpenterHeapCalibration(t *testing.T) {
	if testing.Short() {
		t.Skip("calibration")
	}
	type kindCase struct {
		name           string
		budget, maxObj int
		make           func(i int, worst bool) *unstructured.Unstructured
	}
	cases := []kindCase{
		{KindNodePools, karpenter.BudgetPools, karpenter.MaxPoolObject, karpPoolObject},
		{KindNodeClaims, karpenter.BudgetClaims, karpenter.MaxClaimObject, karpClaimObject},
	}
	for i, ck := range karpenter.ClassKinds {
		a := ck.Adapter
		cases = append(cases, kindCase{karpenterClassKinds[i], karpenter.BudgetClasses, karpenter.MaxClassObject,
			func(i int, worst bool) *unstructured.Unstructured { return karpClassObject(a, i, worst) }})
	}
	total := 0
	for _, kc := range cases {
		total += kc.budget
		project := karpenter.Project(kc.name)
		for _, worst := range []bool{false, true} {
			shape := map[bool]string{false: "typical", true: "at the caps"}[worst]
			first, err := project(kc.make(0, worst))
			if err != nil {
				t.Fatal(err)
			}
			if n := karpenter.SizeOf(first); n > kc.maxObj {
				t.Fatalf("%s %s: one object is %d bytes, over MaxObject %d", kc.name, shape, n, kc.maxObj)
			}
			// each source is built and dropped inside the measurement: what
			// the projection shares with it is retained, the rest is garbage
			before := heapNow()
			var held []any
			encoded, per := 0, 0
			for i := 0; ; i++ {
				v, err := project(kc.make(i, worst))
				if err != nil {
					t.Fatal(err)
				}
				n := karpenter.SizeOf(v)
				if encoded+n > kc.budget {
					break
				}
				held, encoded, per = append(held, v), encoded+n, n
			}
			retained := heapNow() - before
			ratio := float64(retained) / float64(encoded)
			t.Logf("%-16s %-11s %5d objects, %5d encoded bytes each, %8d encoded of %8d budget, heap %9d bytes, %.2f heap per encoded byte",
				kc.name, shape, len(held), per, encoded, kc.budget, retained, ratio)
			if ratio <= 0 || ratio > 10 {
				t.Errorf("%s %s: an implausible ratio %.2f", kc.name, shape, ratio)
			}
			runtime.KeepAlive(held)
		}
	}
	t.Logf("encoded admission budget per cluster, every Karpenter kind served: %d bytes (%.2f MiB)", total, float64(total)/(1<<20))
}
