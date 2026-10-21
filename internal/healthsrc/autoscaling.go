package healthsrc

import (
	"context"
	"sort"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/moomora/klyx/internal/hpa"
	"github.com/moomora/klyx/internal/keda"
	"github.com/moomora/klyx/internal/observe"
)

// HorizontalPodAutoscalers: a shared kind (coverage S9 §2, roadmap C3),
// gated by two consumers - the standalone HPA part, whose demand is the
// core autoscaling/v2 API (always), and KEDA's, whose demand is
// scaledobjects being served. One model and projection serve both; KEDA
// never reads "installed" from the HPA kind being open.
const (
	KindHPAs        = "horizontalpodautoscalers"
	TaskAutoscaling = "autoscaling"
	ConsumerHPAs    = "hpas"
	ConsumerKEDA    = "keda"
	hpaGVR          = "autoscaling/v2/horizontalpodautoscalers"
	hpaEntity       = "HorizontalPodAutoscalers"
)

// SharedHPAKind returns the HPA kind; the connection adds it once.
func SharedHPAKind(d NativeDeps) observe.KindSpec {
	always := func(context.Context) (bool, error) { return true, nil }
	kedaServed := func(ctx context.Context) (bool, error) {
		p, err := d.Served(ctx, keda.Group, keda.ResourceScaledObjects, keda.APIVersion)
		if err != nil {
			return false, err
		}
		return p.Served, nil
	}
	typed := d.Typed
	return observe.KindSpec{Name: KindHPAs, GVR: hpaGVR, Scope: "all namespaces", ExpectedType: &autoscalingv2.HorizontalPodAutoscaler{},
		Project: hpa.Project, Budget: hpa.Budget, MaxObject: hpa.MaxObject, Size: hpa.SizeOf, Version: "v2",
		Consumers: []observe.Consumer{{Name: ConsumerHPAs, Demand: always}, {Name: ConsumerKEDA, Demand: kedaServed}},
		NewListWatch: lw(typed, func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
			return typed.AutoscalingV2().HorizontalPodAutoscalers("").List(ctx, o)
		}, func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
			return typed.AutoscalingV2().HorizontalPodAutoscalers("").Watch(ctx, o)
		})}
}

// Autoscaling returns the standalone HPA task (S9 §4). It counts HPAs no
// ScaledObject controls; a KEDA-owned HPA is its ScaledObject's dependency
// and shows here only as a fact row, never counted twice.
func Autoscaling(d NativeDeps) []observe.DomainSpec {
	return []observe.DomainSpec{{ID: TaskAutoscaling, Parts: []observe.PartSpec{
		{ID: "hpas", Entity: hpaEntity, Scope: "all namespaces", Classifier: "native",
			Counted: []string{KindHPAs}, Consumer: ConsumerHPAs, Evaluate: evalHPAs(d.Cluster), Ledger: true},
	}}}
}

// HPARow is one HPA row: its projection, and for a KEDA-owned HPA the
// ScaledObject that controls it (a fact row, never a verdict).
type HPARow struct {
	HPA *hpa.HPA
	// KEDAOwner names the controlling ScaledObject: the row is a fact, its
	// health judged under that ScaledObject (S9 §2).
	KEDAOwner string
	// Others are the other autoscalers on the same target, with owners.
	Others []string
	// GenerationReported: the controller reports the generation it
	// observed (§4 rule 4's stated limit when not).
	GenerationReported bool
}

// FactOnly: a KEDA-owned HPA's row is a fact (S2b's FactRow).
func (r HPARow) FactOnly() bool { return r.KEDAOwner != "" }

// hpaRef is an HPA's ref at the served autoscaling/v2.
func hpaRef(h *hpa.HPA) refInput {
	return refInput{gvr: hpaGVR, ns: h.Namespace, name: h.Name, uid: h.UID, generation: h.Generation, source: KindHPAs}
}

// hpasOf is the HPA kind's captured objects.
func hpasOf(c observe.Capture) []*hpa.HPA {
	var out []*hpa.HPA
	for _, x := range c.Objects[KindHPAs] {
		if h, ok := x.(*hpa.HPA); ok {
			out = append(out, h)
		}
	}
	return out
}

// OtherAutoscalers names, for each HPA, the other HPAs scaling the same
// target (namespace, group, kind and name), each with its owner.
func OtherAutoscalers(hs []*hpa.HPA) map[string][]string {
	type key struct{ ns, group, kind, name string }
	by := map[key][]*hpa.HPA{}
	for _, h := range hs {
		k := key{h.Namespace, h.Target.Group(), h.Target.Kind, h.Target.Name}
		by[k] = append(by[k], h)
	}
	out := map[string][]string{}
	for _, group := range by {
		if len(group) < 2 {
			continue
		}
		for _, h := range group {
			var others []string
			for _, o := range group {
				if o.UID == h.UID {
					continue
				}
				owner := "plain HPA"
				if so, ok := o.KEDAOwner(); ok {
					owner = "KEDA ScaledObject " + so.Name
				}
				others = append(others, o.Namespace+"/"+o.Name+" ("+owner+")")
			}
			sort.Strings(others)
			out[h.UID] = others
		}
	}
	return out
}

func evalHPAs(cluster string) func(observe.Capture) observe.PartResult {
	return func(c observe.Capture) observe.PartResult {
		hs := hpasOf(c)
		others := OtherAutoscalers(hs)
		var items []nativeItem
		var rows []Row[HPARow]
		for _, h := range hs {
			row := HPARow{HPA: h, Others: others[h.UID], GenerationReported: h.GenerationReported()}
			if so, ok := h.KEDAOwner(); ok {
				row.KEDAOwner = so.Name
				if c.Rows {
					rows = append(rows, Row[HPARow]{Ref: hpaRef(h).rowRef(), Data: row})
				}
				continue
			}
			it := nativeItem{hpa.Rule(h, c.Now, others[h.UID]), hpaRef(h)}
			items = append(items, it)
			if c.Rows {
				rows = append(rows, nativeRow(it, row))
			}
		}
		res := nativeResult(c.RoleCap, cluster, TaskAutoscaling, "cannot scale", items)
		if c.Rows {
			res.Rows = rowSet(rows)
		}
		return res
	}
}
