package fleet

import (
	"context"
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/crd"
	"github.com/moomora/klyx/internal/workloads"
)

// NodeDetail is the drill-down for one node.
type NodeDetail struct {
	Summary     workloads.NodeSummary
	Labels      map[string]string
	Annotations map[string]string
	Taints      []NodeTaint
	Conditions  []crd.Condition
	Events      []crd.Event
	YAML        string
	PodsOnNode  []PodOnNode
}

// NodeTaint is a single taint.
type NodeTaint struct {
	Key    string
	Value  string
	Effect string
}

// PodOnNode is a pod reference for the pods-on-node list.
type PodOnNode struct {
	Namespace string
	Name      string
	Phase     string
}

// ListNodes classifies all nodes with the shared node-summary engine. Served
// from the eager node informer's cache once it has synced - the conn already
// watches exactly this data, so a fresh LIST per render was pure apiserver
// load. Before sync (or in conns built without Start, e.g. tests) it falls
// back to a direct LIST.
func (c *ClusterConn) ListNodes(ctx context.Context) ([]workloads.NodeSummary, error) {
	clk := c.clk
	if clk == nil {
		clk = clock.Real{}
	}

	c.mu.RLock()
	inf := c.nodeInf
	c.mu.RUnlock()
	if inf != nil && inf.HasSynced() {
		items := inf.GetStore().List()
		nodes := make([]corev1.Node, 0, len(items))
		for _, obj := range items {
			if n, ok := obj.(*corev1.Node); ok {
				nodes = append(nodes, *n)
			}
		}
		return workloads.SummarizeNodes(nodes, clk.Now()), nil
	}

	list, err := c.typed.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	return workloads.SummarizeNodes(list.Items, clk.Now()), nil
}

// NodeDetail fetches one node by name and assembles its detail view.
func (c *ClusterConn) NodeDetail(ctx context.Context, name string) (NodeDetail, error) {
	n, err := c.typed.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return NodeDetail{}, err
	}

	clk := c.clk
	if clk == nil {
		clk = clock.Real{}
	}
	now := clk.Now()

	summary := workloads.SummarizeNodes([]corev1.Node{*n}, now)[0]

	labels := n.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	annotations := n.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}

	taints := make([]NodeTaint, 0, len(n.Spec.Taints))
	for _, t := range n.Spec.Taints {
		taints = append(taints, NodeTaint{Key: t.Key, Value: t.Value, Effect: string(t.Effect)})
	}

	conds := make([]crd.Condition, 0, len(n.Status.Conditions))
	for _, nc := range n.Status.Conditions {
		conds = append(conds, crd.Condition{
			Type:    string(nc.Type),
			Status:  string(nc.Status),
			Reason:  nc.Reason,
			Message: nc.Message,
		})
	}

	// YAML via unstructured converter (same approach as PodDetail).
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(n)
	var yamlStr string
	if err == nil {
		if obj["apiVersion"] == nil || obj["apiVersion"] == "" {
			obj["apiVersion"] = "v1"
		}
		if obj["kind"] == nil || obj["kind"] == "" {
			obj["kind"] = "Node"
		}
		yamlStr, _ = crd.ToYAML(obj)
	}

	events := c.instanceEvents(ctx, string(n.GetUID()))

	// Pods running on this node via field selector.
	podsOnNode, _ := c.podsOnNode(ctx, name)

	return NodeDetail{
		Summary:     summary,
		Labels:      labels,
		Annotations: annotations,
		Taints:      taints,
		Conditions:  conds,
		Events:      events,
		YAML:        yamlStr,
		PodsOnNode:  podsOnNode,
	}, nil
}

// podsOnNode returns the pods scheduled on the given node.
func (c *ClusterConn) podsOnNode(ctx context.Context, nodeName string) ([]PodOnNode, error) {
	list, err := c.typed.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		FieldSelector: "spec.nodeName=" + nodeName,
	})
	if err != nil {
		return nil, err
	}
	out := make([]PodOnNode, 0, len(list.Items))
	for i := range list.Items {
		p := &list.Items[i]
		out = append(out, PodOnNode{
			Namespace: p.Namespace,
			Name:      p.Name,
			Phase:     string(p.Status.Phase),
		})
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Namespace != out[b].Namespace {
			return out[a].Namespace < out[b].Namespace
		}
		return out[a].Name < out[b].Name
	})
	return out, nil
}
