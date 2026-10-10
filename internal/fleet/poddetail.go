package fleet

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/crd"
	"github.com/moomora/klyx/internal/workloads"
)

// PodDetail is the drill-down for one pod: summary + spec/status fields the
// list does not carry, plus describe-style events and managedFields-free YAML.
type PodDetail struct {
	Summary        workloads.PodSummary
	Labels         map[string]string
	Annotations    map[string]string
	Conditions     []crd.Condition
	Events         []crd.Event
	YAML           string
	QoSClass       string
	ServiceAccount string
}

// PodDetail fetches one pod by namespace/name and assembles its detail view.
// Events are fetched via the shared instanceEvents helper (newest-first, max 50).
// YAML is produced by converting the typed pod to unstructured and calling
// crd.ToYAML (managedFields stripped) — this avoids a second dynamic GET while
// producing identical output to the CRD detail path.
func (c *ClusterConn) PodDetail(ctx context.Context, namespace, name string) (PodDetail, error) {
	p, err := c.typed.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return PodDetail{}, err
	}

	clk := c.clk
	if clk == nil {
		clk = clock.Real{}
	}
	now := clk.Now()

	// Resolve a ReplicaSet owner to its controller exactly like ListPods does,
	// so the inspector offers "restart deployment" for Deployment pods (the
	// detail read previously kept the raw ReplicaSet and hid the verb).
	sums := workloads.SummarizePods([]corev1.Pod{*p}, now)
	c.resolveReplicaSetOwners(ctx, p.Namespace, sums)
	summary := sums[0]

	labels := p.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	annotations := p.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}

	conds := make([]crd.Condition, 0, len(p.Status.Conditions))
	for _, pc := range p.Status.Conditions {
		conds = append(conds, crd.Condition{
			Type:    string(pc.Type),
			Status:  string(pc.Status),
			Reason:  pc.Reason,
			Message: pc.Message,
		})
	}

	// Convert to unstructured for YAML serialisation (avoids a second network
	// call; produces the same kubectl-style YAML as GetInstanceDetail).
	// Typed objects have empty TypeMeta after a Get; inject apiVersion/kind so
	// the YAML matches kubectl output.
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(p)
	var yamlStr string
	if err == nil {
		if obj["apiVersion"] == nil || obj["apiVersion"] == "" {
			obj["apiVersion"] = "v1"
		}
		if obj["kind"] == nil || obj["kind"] == "" {
			obj["kind"] = "Pod"
		}
		yamlStr, _ = crd.ToYAML(obj)
	}

	events := c.instanceEvents(ctx, string(p.GetUID()))

	return PodDetail{
		Summary:        summary,
		Labels:         labels,
		Annotations:    annotations,
		Conditions:     conds,
		Events:         events,
		YAML:           yamlStr,
		QoSClass:       string(p.Status.QOSClass),
		ServiceAccount: p.Spec.ServiceAccountName,
	}, nil
}
