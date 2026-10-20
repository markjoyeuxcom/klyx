package fleet

import (
	"context"
	"github.com/moomora/klyx/internal/dra"
	"github.com/moomora/klyx/internal/extres"
	"sort"

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
	// Coverage S6: the resizable units, the debug containers, and what the
	// scheduler model assumes.
	Resources       []workloads.ResizeResources
	Ephemeral       []workloads.EphemeralContainer
	EphemeralMore   int
	SchedAssumption string
	// Coverage S11a §6: the pod's extended requests, per container.
	Extended extres.PodRequests
	// Coverage S11b §3.5: the claims the pod references, mapped as the
	// scheduler maps them, and each device's health as the kubelet reports it.
	Claims       []dra.PodClaimRef
	DeviceHealth []DeviceHealth
	// DeviceHealthOmitted counts the status entries past the bound: health
	// is not established for a claim among them.
	DeviceHealthOmitted int
}

// DeviceHealth is one allocatedResourcesStatus entry of one container: a
// claim ("claim:<claim>/<request>") or a device-plugin resource, and each
// device's health. Message is the kubelet's text: local only.
type DeviceHealth struct {
	Container string
	Init      bool
	Name      string
	Devices   []DeviceHealthEntry
	// The counts are over every device the entry reports, the kept ones
	// and the omitted alike (Codex's implementation review, finding 3);
	// the unhealthy are kept first.
	Unhealthy, Unknown, Healthy, DevicesOmitted int
}

// DeviceHealthEntry is one device: its ID (the device's CDI ID for DRA,
// never joined to driver/pool/device), health and message.
type DeviceHealthEntry struct {
	ID, Health, Message string
}

// The pod detail's device-health bounds (S11b §7).
const (
	maxDeviceHealth        = 32
	maxDeviceHealthDevices = 16
)

// deviceHealthOf lists a pod's device health, bounded: each entry's counts
// over all its devices, the unhealthy kept first, and the entries past the
// bound counted.
func deviceHealthOf(p *corev1.Pod) ([]DeviceHealth, int) {
	var out []DeviceHealth
	omitted := 0
	rank := map[corev1.ResourceHealthStatus]int{corev1.ResourceHealthStatusUnhealthy: 0, corev1.ResourceHealthStatusUnknown: 1, corev1.ResourceHealthStatusHealthy: 2}
	add := func(statuses []corev1.ContainerStatus, init bool) {
		for _, cs := range statuses {
			for _, rs := range cs.AllocatedResourcesStatus {
				if len(out) == maxDeviceHealth {
					omitted++
					continue
				}
				h := DeviceHealth{Container: cs.Name, Init: init, Name: string(rs.Name)}
				res := append([]corev1.ResourceHealth(nil), rs.Resources...)
				sort.SliceStable(res, func(i, j int) bool { return rank[res[i].Health] < rank[res[j].Health] })
				for i, r := range res {
					switch r.Health {
					case corev1.ResourceHealthStatusUnhealthy:
						h.Unhealthy++
					case corev1.ResourceHealthStatusHealthy:
						h.Healthy++
					default:
						h.Unknown++
					}
					if i >= maxDeviceHealthDevices {
						h.DevicesOmitted++
						continue
					}
					msg := ""
					if r.Message != nil {
						msg = dra.Clip(*r.Message, dra.MaxMessageBytes)
					}
					h.Devices = append(h.Devices, DeviceHealthEntry{ID: string(r.ResourceID), Health: string(r.Health), Message: msg})
				}
				out = append(out, h)
			}
		}
	}
	add(p.Status.InitContainerStatuses, true)
	add(p.Status.ContainerStatuses, false)
	return out, omitted
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
	sums := workloads.SummarizePodsModel([]corev1.Pod{*p}, now, c.schedModel())
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
	eph, more := workloads.EphemeralOf(p)

	health, healthOmitted := deviceHealthOf(p)
	return PodDetail{
		Summary:             summary,
		Labels:              labels,
		Annotations:         annotations,
		Conditions:          conds,
		Events:              events,
		YAML:                yamlStr,
		QoSClass:            string(p.Status.QOSClass),
		ServiceAccount:      p.Spec.ServiceAccountName,
		Resources:           workloads.ResizeResourcesOf(p),
		Extended:            extres.OfPod(p),
		Claims:              dra.PodClaims(p),
		DeviceHealth:        health,
		DeviceHealthOmitted: healthOmitted,
		Ephemeral:           eph,
		EphemeralMore:       more,
		SchedAssumption:     c.schedModel().Assumption(),
	}, nil
}
