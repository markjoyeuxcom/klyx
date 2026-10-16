package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"

	"github.com/moomora/klyx/internal/crd"
	"github.com/moomora/klyx/internal/urlscrub"
)

// ListCRDs lists the cluster's CustomResourceDefinitions and parses them. A
// single cheap dynamic list; no watch.
func (c *ClusterConn) ListCRDs(ctx context.Context) ([]crd.Info, error) {
	list, err := c.dyn.Resource(crd.GVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]crd.Info, 0, len(list.Items))
	for i := range list.Items {
		u := &unstructured.Unstructured{Object: list.Items[i].Object}
		if info, ok := crd.ParseCRD(u); ok {
			out = append(out, info)
		}
	}
	return out, nil
}

// CountResource returns a hybrid instance count for a kind via a single
// metadata-only list page (Limit=crd.Cap). count is exact below the cap; at the
// cap with a continue token it is the cap and capped=true.
func (c *ClusterConn) CountResource(ctx context.Context, group, version, plural string) (int, bool, error) {
	gvr := schema.GroupVersionResource{Group: group, Version: version, Resource: plural}
	list, err := c.meta.Resource(gvr).List(ctx, metav1.ListOptions{Limit: crd.Cap})
	if err != nil {
		return 0, false, err
	}
	count, capped := crd.CountDisplay(len(list.Items), list.GetContinue())
	return count, capped, nil
}

// ListInstances returns one page of instances of a kind plus the next
// continue token ("" on the last page). A single list page; no watch.
//
// The hand-written lists (a few built-ins and CRD groups, which feed the risk
// filter) come first. Every other kind asks the API server for its table
// (The Line slice 15a): the kind's printer columns, evaluated by the server
// exactly as kubectl get prints them. Without a table (no REST client, a
// server that cannot render one, a failed table read) the metadata-only list
// answers - its rows without columns, or its own error - never a guess.
func (c *ClusterConn) ListInstances(ctx context.Context, group, version, plural string, limit int64, continueToken string) (crd.InstancePage, error) {
	if items, next, handled, err := c.listCurated(ctx, group, version, plural, limit, continueToken); handled {
		return crd.InstancePage{Items: items, Continue: next}, err
	}
	gvr := schema.GroupVersionResource{Group: group, Version: version, Resource: plural}
	payload := crd.IsPayloadResource(group, plural)
	if tbl, ok := c.readTable(ctx, gvr, "", "", limit, continueToken, payload); ok {
		if payload {
			// coverage S0b §3: the server's cells are discarded unread
			if page, err := crd.LocalPage(tbl, c.payloadColumns(ctx, group, plural, version)); err == nil {
				return page, nil
			}
		} else if page, err := crd.FromTable(tbl, crd.BuiltinGroups[group]); err == nil {
			crd.ScrubCells(&page)
			return page, nil
		}
	}
	list, err := c.meta.Resource(gvr).List(ctx, metav1.ListOptions{Limit: limit, Continue: continueToken})
	if err != nil {
		return crd.InstancePage{}, err
	}
	out := make([]crd.InstanceMeta, 0, len(list.Items))
	for i := range list.Items {
		m := &list.Items[i]
		out = append(out, crd.InstanceMeta{
			Namespace: m.GetNamespace(),
			Name:      m.GetName(),
			Created:   m.GetCreationTimestamp().Time,
		})
	}
	return crd.InstancePage{Items: out, Continue: list.GetContinue()}, nil
}

// listCurated answers the kinds with a hand-written list; handled=false for
// every other kind.
func (c *ClusterConn) listCurated(ctx context.Context, group, version, plural string, limit int64, continueToken string) (items []crd.InstanceMeta, next string, handled bool, err error) {
	wrap := func(items []crd.InstanceMeta, next string, err error) ([]crd.InstanceMeta, string, bool, error) {
		return items, next, true, err
	}
	if c.typed != nil && group == "" && version == "v1" && plural == "services" {
		return wrap(c.listServiceInstances(ctx, limit, continueToken))
	}
	if c.typed != nil && group == "" && version == "v1" && plural == "persistentvolumeclaims" {
		return wrap(c.listPVCInstances(ctx, limit, continueToken))
	}
	if c.typed != nil && group == "" && version == "v1" && plural == "persistentvolumes" {
		return wrap(c.listPVInstances(ctx, limit, continueToken))
	}
	if c.typed != nil && group == "" && version == "v1" && plural == "secrets" {
		return wrap(c.listSecretInstances(ctx, limit, continueToken))
	}
	if c.typed != nil && group == "" && version == "v1" && plural == "configmaps" {
		return wrap(c.listConfigMapInstances(ctx, limit, continueToken))
	}
	if c.typed != nil && group == "discovery.k8s.io" && version == "v1" && plural == "endpointslices" {
		return wrap(c.listEndpointSliceInstances(ctx, limit, continueToken))
	}
	if c.typed != nil && group == "networking.k8s.io" && version == "v1" && plural == "ingresses" {
		return wrap(c.listIngressInstances(ctx, limit, continueToken))
	}
	if c.typed != nil && group == "networking.k8s.io" && version == "v1" && plural == "networkpolicies" {
		return wrap(c.listNetworkPolicyInstances(ctx, limit, continueToken))
	}
	if c.typed != nil && group == "autoscaling" && version == "v2" && plural == "horizontalpodautoscalers" {
		return wrap(c.listHPAInstances(ctx, limit, continueToken))
	}
	if c.typed != nil && group == "policy" && version == "v1" && plural == "poddisruptionbudgets" {
		return wrap(c.listPDBInstances(ctx, limit, continueToken))
	}
	if c.typed != nil && group == "batch" && version == "v1" && plural == "jobs" {
		return wrap(c.listJobInstances(ctx, limit, continueToken))
	}
	if c.typed != nil && group == "batch" && version == "v1" && plural == "cronjobs" {
		return wrap(c.listCronJobInstances(ctx, limit, continueToken))
	}
	if c.dyn != nil && usesUnstructuredListFields(group, plural) {
		return wrap(c.listUnstructuredFieldInstances(ctx, group, version, plural, limit, continueToken))
	}

	return nil, "", false, nil
}

// ApplyResourceYAML applies the YAML currently shown in the generic resource
// detail. The expected GVR/GVK/name/namespace checks prevent a sidebar detail
// edit from silently creating or mutating a different object.
func (c *ClusterConn) ApplyResourceYAML(ctx context.Context, group, version, plural, kind, scope, ns, name, expectedUID, yamlDoc string, dryRun bool) error {
	if c.dyn == nil {
		return errors.New("dynamic client unavailable")
	}
	jsonDoc, err := yaml.YAMLToJSON([]byte(yamlDoc))
	if err != nil {
		return fmt.Errorf("parse yaml: %w", err)
	}
	var obj map[string]interface{}
	if err := json.Unmarshal(jsonDoc, &obj); err != nil {
		return fmt.Errorf("parse yaml: %w", err)
	}
	u := &unstructured.Unstructured{Object: obj}
	if err := validateApplyTarget(u, group, version, kind, scope, ns, name); err != nil {
		return err
	}
	if kind == "Secret" && strings.Contains(yamlDoc, "<masked>") {
		return errors.New("masked Secret YAML cannot be applied - reveal or edit secret data outside Klyx")
	}
	// Coverage S0b §5: the inspector's apply never creates, never writes a
	// sanitised copy back, and never races a change. Both documents - the
	// submitted one and the live one - must be edit-eligible, and the live
	// resourceVersion rides along as an atomic precondition.
	if crd.HasPlaceholder(yamlDoc) {
		return fmt.Errorf("this %s carries values Klyx shows masked or omitted; edit it in Git", kind)
	}
	gvr := schema.GroupVersionResource{Group: group, Version: version, Resource: plural}
	resource := c.dyn.Resource(gvr)
	var live *unstructured.Unstructured
	if ns != "" {
		live, err = resource.Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	} else {
		live, err = resource.Get(ctx, name, metav1.GetOptions{})
	}
	if err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("%s %s no longer exists; Klyx's editor does not create objects", kind, name)
		}
		return fmt.Errorf("could not read the live %s before applying: %w", kind, err)
	}
	if expectedUID != "" && string(live.GetUID()) != expectedUID {
		return fmt.Errorf("%s %s was deleted and created again since it was opened; reopen it before editing", kind, name)
	}
	for _, doc := range []map[string]interface{}{live.Object, u.Object} {
		if s := crd.SanitizeEmbedded(group, kind, doc); !s.EditEligible {
			if crd.IsPayloadKind(group, kind) {
				return fmt.Errorf("this %s carries templates, patches or values that Klyx shows sanitised; edit it in Git", kind)
			}
			return fmt.Errorf("this %s has a URL with credentials, which Klyx shows without them; edit it in Git", kind)
		}
	}
	u.SetResourceVersion(live.GetResourceVersion())

	// Force:false is deliberate. Server-side apply with Force:true would STEAL
	// field ownership from Flux/Argo on any object they manage, so the change
	// lands and the controller reverts it on the next reconcile - silent drift,
	// the exact thing this tool exists to make visible. With Force:false a field
	// owned by another manager returns a 409 conflict that names the manager, so
	// the honest "GitOps owns this" state surfaces instead of being papered over.
	opts := metav1.ApplyOptions{FieldManager: "klyx", Force: false}
	if dryRun {
		opts.DryRun = []string{metav1.DryRunAll}
	}
	if ns != "" {
		_, err = resource.Namespace(ns).Apply(ctx, name, u, opts)
	} else {
		_, err = resource.Apply(ctx, name, u, opts)
	}
	if err != nil {
		if apierrors.IsConflict(err) && strings.Contains(err.Error(), "has been modified") {
			return fmt.Errorf("%s %s changed since it was opened; reopen it and try again", kind, name)
		}
		if apierrors.IsConflict(err) {
			// The SSA conflict message already lists the conflicting fields +
			// managers; prefix it so the cause is unmistakable.
			return fmt.Errorf("apply conflict - another controller (e.g. Flux/Argo) owns fields you changed; forcing would be reverted on the next reconcile: %w", err)
		}
		return err
	}
	if !dryRun {
		c.signalRefresh()
	}
	return nil
}

func validateApplyTarget(u *unstructured.Unstructured, group, version, kind, scope, ns, name string) error {
	gvk := u.GroupVersionKind()
	if gvk.Group != group || gvk.Version != version || gvk.Kind != kind {
		wantAPI := version
		if group != "" {
			wantAPI = group + "/" + version
		}
		return fmt.Errorf("yaml target mismatch: want %s %s, got %s %s", wantAPI, kind, u.GetAPIVersion(), u.GetKind())
	}
	if u.GetName() != name {
		return fmt.Errorf("yaml name mismatch: want %q, got %q", name, u.GetName())
	}
	gotNS := u.GetNamespace()
	if scope == "Namespaced" && gotNS != ns {
		return fmt.Errorf("yaml namespace mismatch: want %q, got %q", ns, gotNS)
	}
	if scope == "Cluster" && gotNS != "" {
		return fmt.Errorf("cluster-scoped %s must not set metadata.namespace", kind)
	}
	return nil
}

func (c *ClusterConn) listServiceInstances(ctx context.Context, limit int64, continueToken string) ([]crd.InstanceMeta, string, error) {
	list, err := c.typed.CoreV1().Services("").List(ctx, metav1.ListOptions{Limit: limit, Continue: continueToken})
	if err != nil {
		return nil, "", err
	}
	out := make([]crd.InstanceMeta, 0, len(list.Items))
	for i := range list.Items {
		svc := &list.Items[i]
		out = append(out, crd.InstanceMeta{
			Namespace: svc.Namespace,
			Name:      svc.Name,
			Created:   svc.CreationTimestamp.Time,
			Fields: map[string]string{
				"type":       string(svc.Spec.Type),
				"clusterIP":  serviceClusterIP(svc),
				"externalIP": serviceExternalIP(svc),
				"ports":      servicePorts(svc.Spec.Ports),
			},
		})
	}
	return out, list.GetContinue(), nil
}

func (c *ClusterConn) listEndpointSliceInstances(ctx context.Context, limit int64, continueToken string) ([]crd.InstanceMeta, string, error) {
	list, err := c.typed.DiscoveryV1().EndpointSlices("").List(ctx, metav1.ListOptions{Limit: limit, Continue: continueToken})
	if err != nil {
		return nil, "", err
	}
	out := make([]crd.InstanceMeta, 0, len(list.Items))
	for i := range list.Items {
		slice := &list.Items[i]
		ready, total := endpointSliceReady(slice.Endpoints)
		out = append(out, crd.InstanceMeta{
			Namespace: slice.Namespace,
			Name:      slice.Name,
			Created:   slice.CreationTimestamp.Time,
			Fields: map[string]string{
				"service":     slice.Labels["kubernetes.io/service-name"],
				"addressType": string(slice.AddressType),
				"endpoints":   fmt.Sprintf("%d/%d", ready, total),
				"addresses":   endpointSliceAddresses(slice.Endpoints),
				"ports":       endpointSlicePorts(slice.Ports),
			},
		})
	}
	return out, list.GetContinue(), nil
}

func (c *ClusterConn) listPVCInstances(ctx context.Context, limit int64, continueToken string) ([]crd.InstanceMeta, string, error) {
	list, err := c.typed.CoreV1().PersistentVolumeClaims("").List(ctx, metav1.ListOptions{Limit: limit, Continue: continueToken})
	if err != nil {
		return nil, "", err
	}
	out := make([]crd.InstanceMeta, 0, len(list.Items))
	for i := range list.Items {
		pvc := &list.Items[i]
		out = append(out, crd.InstanceMeta{
			Namespace: pvc.Namespace,
			Name:      pvc.Name,
			Created:   pvc.CreationTimestamp.Time,
			Fields: map[string]string{
				"status": pvcStatus(pvc),
				"class":  pvcStorageClass(pvc),
				"size":   pvcSize(pvc),
				"modes":  accessModes(pvc.Spec.AccessModes),
				"volume": firstNonEmptyString(pvc.Spec.VolumeName, "-"),
			},
		})
	}
	return out, list.GetContinue(), nil
}

func (c *ClusterConn) listPVInstances(ctx context.Context, limit int64, continueToken string) ([]crd.InstanceMeta, string, error) {
	list, err := c.typed.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{Limit: limit, Continue: continueToken})
	if err != nil {
		return nil, "", err
	}
	out := make([]crd.InstanceMeta, 0, len(list.Items))
	for i := range list.Items {
		pv := &list.Items[i]
		out = append(out, crd.InstanceMeta{
			Name:    pv.Name,
			Created: pv.CreationTimestamp.Time,
			Fields: map[string]string{
				"status":  string(pv.Status.Phase),
				"class":   firstNonEmptyString(pv.Spec.StorageClassName, "-"),
				"size":    quantityString(pv.Spec.Capacity.Storage()),
				"modes":   accessModes(pv.Spec.AccessModes),
				"claim":   pvClaim(pv),
				"reclaim": string(pv.Spec.PersistentVolumeReclaimPolicy),
			},
		})
	}
	return out, list.GetContinue(), nil
}

func (c *ClusterConn) listSecretInstances(ctx context.Context, limit int64, continueToken string) ([]crd.InstanceMeta, string, error) {
	list, err := c.typed.CoreV1().Secrets("").List(ctx, metav1.ListOptions{Limit: limit, Continue: continueToken})
	if err != nil {
		return nil, "", err
	}
	out := make([]crd.InstanceMeta, 0, len(list.Items))
	for i := range list.Items {
		s := &list.Items[i]
		out = append(out, crd.InstanceMeta{
			Namespace: s.Namespace,
			Name:      s.Name,
			Created:   s.CreationTimestamp.Time,
			Fields: map[string]string{
				"type":      string(s.Type),
				"keys":      fmt.Sprintf("%d", len(s.Data)),
				"immutable": boolPtr(s.Immutable),
			},
		})
	}
	return out, list.GetContinue(), nil
}

func (c *ClusterConn) listConfigMapInstances(ctx context.Context, limit int64, continueToken string) ([]crd.InstanceMeta, string, error) {
	list, err := c.typed.CoreV1().ConfigMaps("").List(ctx, metav1.ListOptions{Limit: limit, Continue: continueToken})
	if err != nil {
		return nil, "", err
	}
	out := make([]crd.InstanceMeta, 0, len(list.Items))
	for i := range list.Items {
		cm := &list.Items[i]
		out = append(out, crd.InstanceMeta{
			Namespace: cm.Namespace,
			Name:      cm.Name,
			Created:   cm.CreationTimestamp.Time,
			Fields: map[string]string{
				"keys":      fmt.Sprintf("%d", len(cm.Data)+len(cm.BinaryData)),
				"immutable": boolPtr(cm.Immutable),
			},
		})
	}
	return out, list.GetContinue(), nil
}

func (c *ClusterConn) listIngressInstances(ctx context.Context, limit int64, continueToken string) ([]crd.InstanceMeta, string, error) {
	list, err := c.typed.NetworkingV1().Ingresses("").List(ctx, metav1.ListOptions{Limit: limit, Continue: continueToken})
	if err != nil {
		return nil, "", err
	}
	out := make([]crd.InstanceMeta, 0, len(list.Items))
	for i := range list.Items {
		ing := &list.Items[i]
		out = append(out, crd.InstanceMeta{
			Namespace: ing.Namespace,
			Name:      ing.Name,
			Created:   ing.CreationTimestamp.Time,
			Fields: map[string]string{
				"class":    ingressClass(ing),
				"hosts":    ingressHosts(ing),
				"address":  ingressAddress(ing.Status.LoadBalancer.Ingress),
				"tls":      ingressTLS(ing),
				"backends": ingressBackends(ing),
			},
		})
	}
	return out, list.GetContinue(), nil
}

func (c *ClusterConn) listNetworkPolicyInstances(ctx context.Context, limit int64, continueToken string) ([]crd.InstanceMeta, string, error) {
	list, err := c.typed.NetworkingV1().NetworkPolicies("").List(ctx, metav1.ListOptions{Limit: limit, Continue: continueToken})
	if err != nil {
		return nil, "", err
	}
	out := make([]crd.InstanceMeta, 0, len(list.Items))
	for i := range list.Items {
		np := &list.Items[i]
		out = append(out, crd.InstanceMeta{
			Namespace: np.Namespace,
			Name:      np.Name,
			Created:   np.CreationTimestamp.Time,
			Fields: map[string]string{
				"selector":    labelSelectorString(&np.Spec.PodSelector),
				"policyTypes": policyTypes(np.Spec.PolicyTypes),
				"ingress":     fmt.Sprintf("%d", len(np.Spec.Ingress)),
				"egress":      fmt.Sprintf("%d", len(np.Spec.Egress)),
			},
		})
	}
	return out, list.GetContinue(), nil
}

func (c *ClusterConn) listHPAInstances(ctx context.Context, limit int64, continueToken string) ([]crd.InstanceMeta, string, error) {
	list, err := c.typed.AutoscalingV2().HorizontalPodAutoscalers("").List(ctx, metav1.ListOptions{Limit: limit, Continue: continueToken})
	if err != nil {
		return nil, "", err
	}
	out := make([]crd.InstanceMeta, 0, len(list.Items))
	for i := range list.Items {
		h := &list.Items[i]
		out = append(out, crd.InstanceMeta{
			Namespace: h.Namespace,
			Name:      h.Name,
			Created:   h.CreationTimestamp.Time,
			Fields: map[string]string{
				"target":   hpaTarget(h),
				"replicas": hpaReplicas(h),
				"metrics":  hpaMetrics(h),
			},
		})
	}
	return out, list.GetContinue(), nil
}

func (c *ClusterConn) listPDBInstances(ctx context.Context, limit int64, continueToken string) ([]crd.InstanceMeta, string, error) {
	list, err := c.typed.PolicyV1().PodDisruptionBudgets("").List(ctx, metav1.ListOptions{Limit: limit, Continue: continueToken})
	if err != nil {
		return nil, "", err
	}
	out := make([]crd.InstanceMeta, 0, len(list.Items))
	for i := range list.Items {
		pdb := &list.Items[i]
		out = append(out, crd.InstanceMeta{
			Namespace: pdb.Namespace,
			Name:      pdb.Name,
			Created:   pdb.CreationTimestamp.Time,
			Fields: map[string]string{
				"allowed":  fmt.Sprintf("%d", pdb.Status.DisruptionsAllowed),
				"healthy":  fmt.Sprintf("%d/%d", pdb.Status.CurrentHealthy, pdb.Status.DesiredHealthy),
				"expected": fmt.Sprintf("%d", pdb.Status.ExpectedPods),
			},
		})
	}
	return out, list.GetContinue(), nil
}

func (c *ClusterConn) listJobInstances(ctx context.Context, limit int64, continueToken string) ([]crd.InstanceMeta, string, error) {
	list, err := c.typed.BatchV1().Jobs("").List(ctx, metav1.ListOptions{Limit: limit, Continue: continueToken})
	if err != nil {
		return nil, "", err
	}
	out := make([]crd.InstanceMeta, 0, len(list.Items))
	for i := range list.Items {
		job := &list.Items[i]
		out = append(out, crd.InstanceMeta{
			Namespace: job.Namespace,
			Name:      job.Name,
			Created:   job.CreationTimestamp.Time,
			Fields: map[string]string{
				"active":      fmt.Sprintf("%d", job.Status.Active),
				"succeeded":   fmt.Sprintf("%d", job.Status.Succeeded),
				"failed":      fmt.Sprintf("%d", job.Status.Failed),
				"completions": jobCompletions(job),
			},
		})
	}
	return out, list.GetContinue(), nil
}

func (c *ClusterConn) listCronJobInstances(ctx context.Context, limit int64, continueToken string) ([]crd.InstanceMeta, string, error) {
	list, err := c.typed.BatchV1().CronJobs("").List(ctx, metav1.ListOptions{Limit: limit, Continue: continueToken})
	if err != nil {
		return nil, "", err
	}
	out := make([]crd.InstanceMeta, 0, len(list.Items))
	for i := range list.Items {
		cj := &list.Items[i]
		out = append(out, crd.InstanceMeta{
			Namespace: cj.Namespace,
			Name:      cj.Name,
			Created:   cj.CreationTimestamp.Time,
			Fields: map[string]string{
				"schedule":      cj.Spec.Schedule,
				"suspended":     boolPtr(cj.Spec.Suspend),
				"active":        fmt.Sprintf("%d", len(cj.Status.Active)),
				"lastSchedule":  metav1Time(cj.Status.LastScheduleTime),
				"lastSucceeded": metav1Time(cj.Status.LastSuccessfulTime),
			},
		})
	}
	return out, list.GetContinue(), nil
}

func (c *ClusterConn) listUnstructuredFieldInstances(ctx context.Context, group, version, plural string, limit int64, continueToken string) ([]crd.InstanceMeta, string, error) {
	gvr := schema.GroupVersionResource{Group: group, Version: version, Resource: plural}
	list, err := c.dyn.Resource(gvr).List(ctx, metav1.ListOptions{Limit: limit, Continue: continueToken})
	if err != nil {
		return nil, "", err
	}
	out := make([]crd.InstanceMeta, 0, len(list.Items))
	for i := range list.Items {
		u := &list.Items[i]
		out = append(out, crd.InstanceMeta{
			Namespace: u.GetNamespace(),
			Name:      u.GetName(),
			Created:   u.GetCreationTimestamp().Time,
			Fields:    unstructuredFields(group, plural, u),
		})
	}
	return out, list.GetContinue(), nil
}

func usesUnstructuredListFields(group, plural string) bool {
	return group == "cert-manager.io" ||
		group == "external-secrets.io" ||
		group == "helm.toolkit.fluxcd.io" ||
		group == "kustomize.toolkit.fluxcd.io" ||
		(group == "cilium.io" && (plural == "ciliumnetworkpolicies" || plural == "ciliumclusterwidenetworkpolicies"))
}

func serviceClusterIP(svc *corev1.Service) string {
	if svc.Spec.ClusterIP != "" {
		return svc.Spec.ClusterIP
	}
	return "-"
}

func serviceExternalIP(svc *corev1.Service) string {
	values := make([]string, 0, len(svc.Status.LoadBalancer.Ingress)+len(svc.Spec.ExternalIPs)+1)
	for _, ing := range svc.Status.LoadBalancer.Ingress {
		if ing.IP != "" {
			values = append(values, ing.IP)
		} else if ing.Hostname != "" {
			values = append(values, ing.Hostname)
		}
	}
	values = append(values, svc.Spec.ExternalIPs...)
	if svc.Spec.Type == corev1.ServiceTypeExternalName && svc.Spec.ExternalName != "" {
		values = append(values, svc.Spec.ExternalName)
	}
	if len(values) > 0 {
		return strings.Join(values, ", ")
	}
	if svc.Spec.Type == corev1.ServiceTypeLoadBalancer {
		return "pending"
	}
	return "-"
}

func servicePorts(ports []corev1.ServicePort) string {
	if len(ports) == 0 {
		return "-"
	}
	out := make([]string, 0, len(ports))
	for _, p := range ports {
		piece := fmt.Sprintf("%d/%s", p.Port, p.Protocol)
		if p.Name != "" {
			piece = p.Name + " " + piece
		}
		if p.TargetPort.String() != "" && p.TargetPort.String() != "0" {
			piece += "->" + p.TargetPort.String()
		}
		if p.NodePort > 0 {
			piece += fmt.Sprintf(" node:%d", p.NodePort)
		}
		out = append(out, piece)
	}
	return strings.Join(out, ", ")
}

func endpointSliceReady(endpoints []discoveryv1.Endpoint) (ready, total int) {
	for _, ep := range endpoints {
		total++
		if ep.Conditions.Ready == nil || *ep.Conditions.Ready {
			ready++
		}
	}
	return ready, total
}

func endpointSliceAddresses(endpoints []discoveryv1.Endpoint) string {
	const maxAddrs = 4
	values := []string{}
	for _, ep := range endpoints {
		for _, addr := range ep.Addresses {
			values = append(values, addr)
			if len(values) == maxAddrs {
				break
			}
		}
		if len(values) == maxAddrs {
			break
		}
	}
	if len(values) == 0 {
		return "-"
	}
	total := 0
	for _, ep := range endpoints {
		total += len(ep.Addresses)
	}
	s := strings.Join(values, ", ")
	if total > len(values) {
		s += fmt.Sprintf(" +%d", total-len(values))
	}
	return s
}

func endpointSlicePorts(ports []discoveryv1.EndpointPort) string {
	if len(ports) == 0 {
		return "-"
	}
	out := make([]string, 0, len(ports))
	for _, p := range ports {
		port := "-"
		if p.Port != nil {
			port = fmt.Sprintf("%d", *p.Port)
		}
		proto := string(corev1.ProtocolTCP)
		if p.Protocol != nil {
			proto = string(*p.Protocol)
		}
		piece := port + "/" + proto
		if p.Name != nil && *p.Name != "" {
			piece = *p.Name + " " + piece
		}
		out = append(out, piece)
	}
	return strings.Join(out, ", ")
}

func boolPtr(v *bool) string {
	if v == nil {
		return "no"
	}
	if *v {
		return "yes"
	}
	return "no"
}

func quantityString(q *resource.Quantity) string {
	if q == nil || q.IsZero() {
		return "-"
	}
	return q.String()
}

func firstNonEmptyString(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}

func pvcStatus(pvc *corev1.PersistentVolumeClaim) string {
	if pvc.Status.Phase != "" {
		return string(pvc.Status.Phase)
	}
	return "Pending"
}

func pvcStorageClass(pvc *corev1.PersistentVolumeClaim) string {
	if pvc.Spec.StorageClassName != nil && *pvc.Spec.StorageClassName != "" {
		return *pvc.Spec.StorageClassName
	}
	return "-"
}

func pvcSize(pvc *corev1.PersistentVolumeClaim) string {
	return quantityString(pvc.Spec.Resources.Requests.Storage())
}

func accessModes(modes []corev1.PersistentVolumeAccessMode) string {
	if len(modes) == 0 {
		return "-"
	}
	labels := make([]string, 0, len(modes))
	for _, m := range modes {
		switch m {
		case corev1.ReadWriteOnce:
			labels = append(labels, "RWO")
		case corev1.ReadOnlyMany:
			labels = append(labels, "ROX")
		case corev1.ReadWriteMany:
			labels = append(labels, "RWX")
		case corev1.ReadWriteOncePod:
			labels = append(labels, "RWOP")
		default:
			labels = append(labels, string(m))
		}
	}
	return strings.Join(labels, ",")
}

func pvClaim(pv *corev1.PersistentVolume) string {
	if pv.Spec.ClaimRef == nil || pv.Spec.ClaimRef.Name == "" {
		return "-"
	}
	if pv.Spec.ClaimRef.Namespace != "" {
		return pv.Spec.ClaimRef.Namespace + "/" + pv.Spec.ClaimRef.Name
	}
	return pv.Spec.ClaimRef.Name
}

func ingressClass(ing *networkingv1.Ingress) string {
	if ing.Spec.IngressClassName != nil && *ing.Spec.IngressClassName != "" {
		return *ing.Spec.IngressClassName
	}
	if v := ing.Annotations["kubernetes.io/ingress.class"]; v != "" {
		return v
	}
	return "-"
}

func ingressAddress(items []networkingv1.IngressLoadBalancerIngress) string {
	values := make([]string, 0, len(items))
	for _, ing := range items {
		if ing.IP != "" {
			values = append(values, ing.IP)
		} else if ing.Hostname != "" {
			values = append(values, ing.Hostname)
		}
	}
	return joinOrDash(values)
}

func ingressHosts(ing *networkingv1.Ingress) string {
	hosts := make([]string, 0, len(ing.Spec.Rules))
	for _, r := range ing.Spec.Rules {
		if r.Host != "" {
			hosts = append(hosts, r.Host)
		}
	}
	return joinCap(hosts, 3)
}

func ingressTLS(ing *networkingv1.Ingress) string {
	values := make([]string, 0, len(ing.Spec.TLS))
	for _, t := range ing.Spec.TLS {
		if t.SecretName != "" {
			values = append(values, t.SecretName)
			continue
		}
		values = append(values, t.Hosts...)
	}
	return joinCap(values, 2)
}

func ingressBackends(ing *networkingv1.Ingress) string {
	values := []string{}
	if ing.Spec.DefaultBackend != nil {
		if b := ingressBackend(*ing.Spec.DefaultBackend); b != "" {
			values = append(values, b)
		}
	}
	for _, r := range ing.Spec.Rules {
		if r.HTTP == nil {
			continue
		}
		for _, p := range r.HTTP.Paths {
			if b := ingressBackend(p.Backend); b != "" {
				values = append(values, b)
			}
		}
	}
	return joinCap(uniqueStrings(values), 3)
}

func ingressBackend(b networkingv1.IngressBackend) string {
	if b.Service == nil {
		if b.Resource != nil {
			return b.Resource.Kind + "/" + b.Resource.Name
		}
		return ""
	}
	if b.Service.Port.Name != "" {
		return b.Service.Name + ":" + b.Service.Port.Name
	}
	if b.Service.Port.Number > 0 {
		port := fmt.Sprintf("%d", b.Service.Port.Number)
		return b.Service.Name + ":" + port
	}
	return b.Service.Name
}

func labelSelectorString(sel *metav1.LabelSelector) string {
	if sel == nil {
		return "{}"
	}
	s := metav1.FormatLabelSelector(sel)
	if s == "" {
		return "{}"
	}
	return s
}

func policyTypes(types []networkingv1.PolicyType) string {
	if len(types) == 0 {
		return "-"
	}
	out := make([]string, 0, len(types))
	for _, t := range types {
		out = append(out, string(t))
	}
	return strings.Join(out, ",")
}

func hpaTarget(h *autoscalingv2.HorizontalPodAutoscaler) string {
	ref := h.Spec.ScaleTargetRef
	if ref.Kind == "" && ref.Name == "" {
		return "-"
	}
	return ref.Kind + "/" + ref.Name
}

func hpaReplicas(h *autoscalingv2.HorizontalPodAutoscaler) string {
	min := int32(1)
	if h.Spec.MinReplicas != nil {
		min = *h.Spec.MinReplicas
	}
	return fmt.Sprintf("%d/%d/%d/%d", min, h.Status.CurrentReplicas, h.Status.DesiredReplicas, h.Spec.MaxReplicas)
}

func hpaMetrics(h *autoscalingv2.HorizontalPodAutoscaler) string {
	if len(h.Spec.Metrics) == 0 {
		return "-"
	}
	current := map[string]string{}
	for _, m := range h.Status.CurrentMetrics {
		if m.Type == autoscalingv2.ResourceMetricSourceType && m.Resource != nil {
			current[string(m.Resource.Name)] = metricValue(m.Resource.Current)
		}
	}
	values := make([]string, 0, len(h.Spec.Metrics))
	for _, m := range h.Spec.Metrics {
		if m.Type != autoscalingv2.ResourceMetricSourceType || m.Resource == nil {
			continue
		}
		name := string(m.Resource.Name)
		cur := firstNonEmptyString(current[name], "-")
		values = append(values, fmt.Sprintf("%s %s/%s", name, cur, metricTarget(m.Resource.Target)))
	}
	return joinCap(values, 2)
}

func metricTarget(t autoscalingv2.MetricTarget) string {
	if t.AverageUtilization != nil {
		return fmt.Sprintf("%d%%", *t.AverageUtilization)
	}
	if t.AverageValue != nil {
		return t.AverageValue.String()
	}
	if t.Value != nil {
		return t.Value.String()
	}
	return "-"
}

func metricValue(v autoscalingv2.MetricValueStatus) string {
	if v.AverageUtilization != nil {
		return fmt.Sprintf("%d%%", *v.AverageUtilization)
	}
	if v.AverageValue != nil {
		return v.AverageValue.String()
	}
	if v.Value != nil {
		return v.Value.String()
	}
	return "-"
}

func jobCompletions(job *batchv1.Job) string {
	if job.Spec.Completions == nil {
		return fmt.Sprintf("%d/-", job.Status.Succeeded)
	}
	return fmt.Sprintf("%d/%d", job.Status.Succeeded, *job.Spec.Completions)
}

func metav1Time(t *metav1.Time) string {
	if t == nil || t.IsZero() {
		return "-"
	}
	return t.Time.Format("2006-01-02 15:04")
}

func unstructuredFields(group, plural string, u *unstructured.Unstructured) map[string]string {
	if group == "cert-manager.io" {
		return certManagerFields(plural, u)
	}
	if group == "external-secrets.io" {
		return externalSecretsFields(plural, u)
	}
	if group == "helm.toolkit.fluxcd.io" {
		return fluxHelmReleaseFields(plural, u)
	}
	if group == "kustomize.toolkit.fluxcd.io" {
		return fluxKustomizationFields(plural, u)
	}
	if group == "cilium.io" {
		return ciliumPolicyFields(plural, u)
	}
	return nil
}

func certManagerFields(plural string, u *unstructured.Unstructured) map[string]string {
	switch plural {
	case "certificates":
		return map[string]string{
			"ready":   readyText(conditionStatus(u.Object, "Ready")),
			"issuer":  certIssuer(u),
			"expires": dateField(nestedString(u.Object, "status", "notAfter")),
			"renew":   firstNonEmptyString(dateField(nestedString(u.Object, "status", "renewalTime")), nestedString(u.Object, "spec", "renewBefore")),
			"dns":     certDNSNames(u),
		}
	case "certificaterequests":
		return map[string]string{
			"ready":    readyText(conditionStatus(u.Object, "Ready")),
			"issuer":   certIssuer(u),
			"approved": readyText(conditionStatus(u.Object, "Approved")),
			"denied":   readyText(conditionStatus(u.Object, "Denied")),
			"duration": nestedStringOrDash(u.Object, "spec", "duration"),
		}
	case "issuers", "clusterissuers":
		return map[string]string{
			"ready":  readyText(conditionStatus(u.Object, "Ready")),
			"type":   issuerType(u),
			"server": issuerServer(u),
		}
	default:
		return nil
	}
}

func ciliumPolicyFields(plural string, u *unstructured.Unstructured) map[string]string {
	scope := "namespace"
	if plural == "ciliumclusterwidenetworkpolicies" {
		scope = "cluster"
	}
	return map[string]string{
		"selector": selectorSummary(u.Object, "spec", "endpointSelector"),
		"ingress":  ruleCountWithDeny(u.Object, "ingress", "ingressDeny"),
		"egress":   ruleCountWithDeny(u.Object, "egress", "egressDeny"),
		"scope":    scope,
	}
}

func externalSecretsFields(plural string, u *unstructured.Unstructured) map[string]string {
	switch plural {
	case "externalsecrets":
		return map[string]string{
			"ready":   readyText(conditionStatus(u.Object, "Ready")),
			"store":   externalSecretStoreRef(u),
			"target":  firstNonEmptyString(nestedString(u.Object, "spec", "target", "name"), u.GetName()),
			"refresh": nestedStringOrDash(u.Object, "spec", "refreshInterval"),
			"synced":  firstNonEmptyString(dateField(nestedString(u.Object, "status", "refreshTime")), "-"),
		}
	case "secretstores", "clustersecretstores":
		return map[string]string{
			"ready":      readyText(conditionStatus(u.Object, "Ready")),
			"provider":   providerType(u.Object, "spec", "provider"),
			"controller": nestedStringOrDash(u.Object, "spec", "controller"),
		}
	default:
		return nil
	}
}

func fluxHelmReleaseFields(plural string, u *unstructured.Unstructured) map[string]string {
	if plural != "helmreleases" {
		return nil
	}
	return map[string]string{
		"ready":     readyText(conditionStatus(u.Object, "Ready")),
		"suspended": boolField(u.Object, "spec", "suspend"),
		"chart":     nestedStringOrDash(u.Object, "spec", "chart", "spec", "chart"),
		"source":    sourceRef(u.Object, "spec", "chart", "spec", "sourceRef"),
		"revision":  fluxRevision(u.Object),
	}
}

func fluxKustomizationFields(plural string, u *unstructured.Unstructured) map[string]string {
	if plural != "kustomizations" {
		return nil
	}
	return map[string]string{
		"ready":     readyText(conditionStatus(u.Object, "Ready")),
		"suspended": boolField(u.Object, "spec", "suspend"),
		"source":    sourceRef(u.Object, "spec", "sourceRef"),
		"path":      nestedStringOrDash(u.Object, "spec", "path"),
		"revision":  fluxRevision(u.Object),
	}
}

func certIssuer(u *unstructured.Unstructured) string {
	name := nestedString(u.Object, "spec", "issuerRef", "name")
	if name == "" {
		return "-"
	}
	kind := nestedString(u.Object, "spec", "issuerRef", "kind")
	if kind == "" {
		kind = "Issuer"
	}
	return kind + "/" + name
}

func externalSecretStoreRef(u *unstructured.Unstructured) string {
	name := nestedString(u.Object, "spec", "secretStoreRef", "name")
	if name == "" {
		return "-"
	}
	kind := nestedString(u.Object, "spec", "secretStoreRef", "kind")
	if kind == "" {
		kind = "SecretStore"
	}
	return kind + "/" + name
}

func sourceRef(obj map[string]interface{}, fields ...string) string {
	name := nestedString(obj, append(fields, "name")...)
	if name == "" {
		return "-"
	}
	kind := nestedString(obj, append(fields, "kind")...)
	if kind == "" {
		kind = "Source"
	}
	return kind + "/" + name
}

func fluxRevision(obj map[string]interface{}) string {
	return firstNonEmptyString(
		nestedString(obj, "status", "lastAppliedRevision"),
		firstNonEmptyString(nestedString(obj, "status", "lastAttemptedRevision"), "-"),
	)
}

func providerType(obj map[string]interface{}, fields ...string) string {
	m, ok, _ := unstructured.NestedMap(obj, fields...)
	if !ok || len(m) == 0 {
		return "-"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys[0]
}

func boolField(obj map[string]interface{}, fields ...string) string {
	v, ok, _ := unstructured.NestedBool(obj, fields...)
	if !ok || !v {
		return "no"
	}
	return "yes"
}

func certDNSNames(u *unstructured.Unstructured) string {
	values, _, _ := unstructured.NestedStringSlice(u.Object, "spec", "dnsNames")
	if cn := nestedString(u.Object, "spec", "commonName"); cn != "" {
		values = append([]string{cn}, values...)
	}
	return joinCap(uniqueStrings(values), 3)
}

func issuerType(u *unstructured.Unstructured) string {
	spec, ok, _ := unstructured.NestedMap(u.Object, "spec")
	if !ok {
		return "-"
	}
	for _, k := range []string{"acme", "ca", "selfSigned", "vault", "venafi"} {
		if _, ok := spec[k]; ok {
			return k
		}
	}
	return "-"
}

func issuerServer(u *unstructured.Unstructured) string {
	for _, path := range [][]string{{"spec", "acme", "server"}, {"spec", "vault", "server"}, {"spec", "venafi", "tpp", "url"}} {
		if v := nestedString(u.Object, path...); v != "" {
			return v
		}
	}
	return "-"
}

func conditionStatus(obj map[string]interface{}, condType string) string {
	conds, _, _ := unstructured.NestedSlice(obj, "status", "conditions")
	for _, c := range conds {
		m, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if t, _ := m["type"].(string); t == condType {
			status, _ := m["status"].(string)
			return status
		}
	}
	return ""
}

func readyText(status string) string {
	switch status {
	case "True":
		return "ready"
	case "False":
		return "not ready"
	case "Unknown":
		return "unknown"
	default:
		return "-"
	}
}

func dateField(v string) string {
	if v == "" {
		return ""
	}
	if len(v) >= len("2006-01-02") {
		return v[:len("2006-01-02")]
	}
	return v
}

func nestedString(obj map[string]interface{}, fields ...string) string {
	v, _, _ := unstructured.NestedString(obj, fields...)
	return v
}

func nestedStringOrDash(obj map[string]interface{}, fields ...string) string {
	return firstNonEmptyString(nestedString(obj, fields...), "-")
}

func selectorSummary(obj map[string]interface{}, fields ...string) string {
	sel, ok, _ := unstructured.NestedMap(obj, fields...)
	if !ok || len(sel) == 0 {
		return "{}"
	}
	labels, _, _ := unstructured.NestedStringMap(sel, "matchLabels")
	parts := make([]string, 0, len(labels)+1)
	for k, v := range labels {
		parts = append(parts, k+"="+v)
	}
	sort.Strings(parts)
	if exprs, ok, _ := unstructured.NestedSlice(sel, "matchExpressions"); ok && len(exprs) > 0 {
		parts = append(parts, fmt.Sprintf("exprs:%d", len(exprs)))
	}
	if len(parts) == 0 {
		return "{}"
	}
	return strings.Join(parts, ",")
}

func ruleCountWithDeny(obj map[string]interface{}, allowField, denyField string) string {
	allow, _, _ := unstructured.NestedSlice(obj, "spec", allowField)
	deny, _, _ := unstructured.NestedSlice(obj, "spec", denyField)
	if len(deny) > 0 {
		return fmt.Sprintf("%d +%d deny", len(allow), len(deny))
	}
	return fmt.Sprintf("%d", len(allow))
}

func joinOrDash(values []string) string {
	if len(values) == 0 {
		return "-"
	}
	return strings.Join(values, ", ")
}

func joinCap(values []string, capAt int) string {
	values = uniqueStrings(values)
	if len(values) == 0 {
		return "-"
	}
	if len(values) <= capAt {
		return strings.Join(values, ", ")
	}
	return strings.Join(values[:capAt], ", ") + fmt.Sprintf(" +%d", len(values)-capAt)
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// GetInstanceDetail fetches one object (full YAML + conditions + header) plus its
// describe-style Events (filtered by involvedObject.uid). Snapshot; no watch.
func (c *ClusterConn) GetInstanceDetail(ctx context.Context, group, version, plural, ns, name string) (crd.InstanceDetail, error) {
	gvr := schema.GroupVersionResource{Group: group, Version: version, Resource: plural}
	// One read for the object and its table row (The Line slice 15b, design
	// review 1): asked as a table with includeObject=Object, one response
	// carries the printer-column cells and the whole object - one snapshot.
	// Without a table, the plain GET answers and there is no summary.
	u, summary := c.objectWithSummary(ctx, gvr, ns, name)
	if u == nil {
		var err error
		if ns == "" {
			u, err = c.dyn.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
		} else {
			u, err = c.dyn.Resource(gvr).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
		}
		if err != nil {
			return crd.InstanceDetail{}, err
		}
	}

	// Mask secret values before producing YAML and building the DTO. The masking
	// happens at the unstructured level so the raw object is never passed to
	// ToYAML with live data, and secret key info travels separately.
	obj := u.Object
	var secretKeys []crd.SecretKeyInfo
	if group == "" && version == "v1" && plural == "secrets" {
		obj, secretKeys = crd.MaskSecretData(obj)
	}
	// Coverage S0b: the embedded-payload sanitiser - templates, patches,
	// inline values and embedded Secrets of the registered kinds, and URL
	// user info on every kind - runs before any DTO field is made.
	sz := crd.SanitizeEmbedded(group, u.GetKind(), obj)
	obj = sz.Object
	// Every DTO field below is read from the SANITISED object, never from u:
	// a client-side-applied Secret carries its full base64 data in the
	// kubectl.kubernetes.io/last-applied-configuration annotation, which
	// MaskSecretData strips from obj only. Reading annotations from u leaked
	// it past the explicit reveal flow (review 2026-10-03 #2).
	sanitised := &unstructured.Unstructured{Object: obj}

	// Build service backing for v1 Services: typed-get the Service (for
	// spec.ports and spec.selector) then list its EndpointSlices. A failure here
	// is non-fatal; backing will be nil and the detail still renders.
	var serviceBacking *crd.ServiceBacking
	if group == "" && version == "v1" && plural == "services" {
		backing := c.buildServiceBacking(ctx, ns, name)
		serviceBacking = &backing
	}

	// Build HPA scaling for autoscaling HorizontalPodAutoscalers (any version;
	// we list v2 but the unstructured parse works regardless of version). A
	// parse error here is non-fatal; hpaScaling will be nil and the detail
	// still renders, mirroring how serviceBacking degrades.
	var hpaScaling *crd.HPAScaling
	if group == "autoscaling" && plural == "horizontalpodautoscalers" {
		if s, err := crd.BuildHPAScaling(u); err == nil {
			hpaScaling = s
		}
	}

	y, _ := crd.ToYAML(obj)
	d := crd.InstanceDetail{
		Kind:           u.GetKind(),
		Namespace:      ns,
		Name:           name,
		UID:            string(u.GetUID()),
		Generation:     u.GetGeneration(),
		Created:        u.GetCreationTimestamp().Time,
		Labels:         sanitised.GetLabels(),
		Annotations:    sanitised.GetAnnotations(),
		Conditions:     crd.ParseConditionsDeep(group, plural, obj),
		YAML:           y,
		SecretKeys:     secretKeys,
		ServiceBacking: serviceBacking,
		HPAScaling:     hpaScaling,
		Related:        c.relatedRefs(ctx, group, version, plural, ns, name, u),
		// Facts are built from the sanitised object (masked for Secrets), so no
		// secret value can reach a fact even via a future buggy builder.
		Facts: crd.BuildFacts(group, plural, sanitised),
	}
	d.Facts = append(d.Facts, c.rbacDanglingFacts(ctx, group, plural, ns, u)...)
	d.Events = c.instanceEvents(ctx, string(u.GetUID()))
	// The Line slice 15b: everything below reads the sanitised object.
	// Coverage S0b §3: a registered payload kind's Summary is evaluated here
	// over the sanitised object - the server's cells are discarded - and
	// every other kind's cells pass the URL rule.
	if crd.IsPayloadResource(group, plural) {
		summary = nil
		if cols := c.payloadColumns(ctx, group, plural, version); cols != nil {
			summary = crd.LocalSummary(cols, obj)
		}
	} else {
		for i := range summary {
			summary[i].Value = urlscrub.Text(summary[i].Value).Text
		}
	}
	d.Summary = summary
	d.EditEligible = sz.EditEligible
	d.DescribeMode = sz.DescribeMode
	d.ObservedGeneration = crd.ObservedGenerationOf(obj)
	if ts := sanitised.GetDeletionTimestamp(); ts != nil {
		d.Deleting = ts.Time
	}
	d.Finalizers = sanitised.GetFinalizers()
	for _, o := range sanitised.GetOwnerReferences() {
		d.Owners = append(d.Owners, crd.OwnerRef{APIVersion: o.APIVersion, Kind: o.Kind, Name: o.Name, Controller: o.Controller != nil && *o.Controller})
	}
	d.Managers = crd.ManagersOf(obj)
	d.FieldsJSON, d.FieldsOmitted = crd.FieldsJSONOf(obj, group == "" && version == "v1" && plural == "secrets")
	return d, nil
}

// objectWithSummary reads one object as its table with the whole object in
// the row. nil when no table is to be had, or the row is not the named
// object: the caller then reads it plainly.
func (c *ClusterConn) objectWithSummary(ctx context.Context, gvr schema.GroupVersionResource, ns, name string) (*unstructured.Unstructured, []crd.SummaryCell) {
	tbl, ok := c.readTable(ctx, gvr, ns, name, 0, "", true)
	if !ok || len(tbl.Rows) != 1 || len(tbl.Rows[0].Object.Raw) == 0 {
		return nil, nil
	}
	u := &unstructured.Unstructured{}
	if err := u.UnmarshalJSON(tbl.Rows[0].Object.Raw); err != nil || u.GetName() != name || u.GetKind() == "" || u.GetKind() == "PartialObjectMetadata" {
		return nil, nil
	}
	cols, keep := crd.TableColumns(tbl, crd.BuiltinGroups[gvr.Group])
	cells := crd.RowCells(tbl.Rows[0], keep)
	summary := make([]crd.SummaryCell, 0, len(cols))
	for i, col := range cols {
		summary = append(summary, crd.SummaryCell{Column: col, Value: cells[i]})
	}
	return u, summary
}

func (c *ClusterConn) relatedRefs(ctx context.Context, group, version, plural, ns, name string, u *unstructured.Unstructured) []crd.RelatedRef {
	out := []crd.RelatedRef{}
	add := func(ref crd.RelatedRef) {
		if ref.Name == "" || ref.Kind == "" || ref.Plural == "" {
			return
		}
		for _, existing := range out {
			if existing.Group == ref.Group && existing.Version == ref.Version && existing.Plural == ref.Plural &&
				existing.Namespace == ref.Namespace && existing.Name == ref.Name && existing.Relation == ref.Relation {
				return
			}
		}
		out = append(out, ref)
	}

	for _, owner := range ownerRefs(ns, u) {
		add(owner)
	}

	switch {
	case group == "" && version == "v1" && plural == "services":
		for _, ref := range c.serviceRelatedRefs(ctx, ns, name) {
			add(ref)
		}
	case group == "discovery.k8s.io" && version == "v1" && plural == "endpointslices":
		if svc := u.GetLabels()["kubernetes.io/service-name"]; svc != "" {
			add(resourceRef("Service", ns, svc, "", "v1", "services", "Namespaced", "service"))
		}
	case group == "autoscaling" && plural == "horizontalpodautoscalers":
		kind := nestedString(u.Object, "spec", "scaleTargetRef", "kind")
		target := nestedString(u.Object, "spec", "scaleTargetRef", "name")
		if ref, ok := kindResourceRef(kind, ns, target, "scale target"); ok {
			add(ref)
		}
	case group == "cert-manager.io" && plural == "certificates":
		if secret := nestedString(u.Object, "spec", "secretName"); secret != "" {
			add(resourceRef("Secret", ns, secret, "", "v1", "secrets", "Namespaced", "certificate secret"))
		}
		if nextSecret := nestedString(u.Object, "status", "nextPrivateKeySecretName"); nextSecret != "" {
			add(resourceRef("Secret", ns, nextSecret, "", "v1", "secrets", "Namespaced", "next private key"))
		}
		if ref, ok := issuerRelatedRef(u, ns, "issuer"); ok {
			add(ref)
		}
	case group == "cert-manager.io" && plural == "certificaterequests":
		if ref, ok := issuerRelatedRef(u, ns, "issuer"); ok {
			add(ref)
		}
	case group == "" && version == "v1" && plural == "persistentvolumeclaims":
		if pv := nestedString(u.Object, "spec", "volumeName"); pv != "" {
			add(resourceRef("PersistentVolume", "", pv, "", "v1", "persistentvolumes", "Cluster", "bound volume"))
		}
		for _, ref := range c.pvcConsumerRefs(ctx, ns, name) {
			add(ref)
		}
	case group == "" && version == "v1" && plural == "serviceaccounts":
		for _, ref := range c.serviceAccountBindingRefs(ctx, ns, name) {
			add(ref)
		}
	case group == "" && version == "v1" && plural == "configmaps":
		for _, ref := range c.reloaderConsumerRefs(ctx, ns, name, "ConfigMap", u.GetLabels()) {
			add(ref)
		}
	case group == "" && version == "v1" && plural == "secrets":
		for _, ref := range c.reloaderConsumerRefs(ctx, ns, name, "Secret", u.GetLabels()) {
			add(ref)
		}
	case group == "rbac.authorization.k8s.io" && plural == "rolebindings":
		if roleName := nestedString(u.Object, "roleRef", "name"); roleName != "" {
			if nestedString(u.Object, "roleRef", "kind") == "ClusterRole" {
				add(resourceRef("ClusterRole", "", roleName, "rbac.authorization.k8s.io", "v1", "clusterroles", "Cluster", "grants role"))
			} else {
				add(resourceRef("Role", ns, roleName, "rbac.authorization.k8s.io", "v1", "roles", "Namespaced", "grants role"))
			}
		}
	case group == "rbac.authorization.k8s.io" && plural == "clusterrolebindings":
		if roleName := nestedString(u.Object, "roleRef", "name"); roleName != "" {
			add(resourceRef("ClusterRole", "", roleName, "rbac.authorization.k8s.io", "v1", "clusterroles", "Cluster", "grants role"))
		}
	case group == "policy" && plural == "poddisruptionbudgets":
		ml, _, _ := unstructured.NestedStringMap(u.Object, "spec", "selector", "matchLabels")
		for _, ref := range c.pdbGuardedRefs(ctx, ns, ml) {
			add(ref)
		}
	case group == "batch" && version == "v1" && plural == "jobs":
		for _, ref := range c.ownerPodRefs(ctx, ns, string(u.GetUID()), "job pod") {
			add(ref)
		}
	case group == "apps" && version == "v1" && plural == "replicasets":
		for _, ref := range c.ownerPodRefs(ctx, ns, string(u.GetUID()), "replica pod") {
			add(ref)
		}
	case group == "" && version == "v1" && plural == "persistentvolumes":
		claimNS := nestedString(u.Object, "spec", "claimRef", "namespace")
		claimName := nestedString(u.Object, "spec", "claimRef", "name")
		if claimName != "" {
			add(resourceRef("PersistentVolumeClaim", claimNS, claimName, "", "v1", "persistentvolumeclaims", "Namespaced", "claim"))
		}
	case group == "networking.k8s.io" && version == "v1" && plural == "ingresses":
		for _, svc := range ingressServiceRefs(u, ns) {
			add(svc)
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Relation != out[j].Relation {
			return out[i].Relation < out[j].Relation
		}
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func resourceRef(kind, namespace, name, group, version, plural, scope, relation string) crd.RelatedRef {
	return crd.RelatedRef{
		Kind: kind, Namespace: namespace, Name: name,
		Group: group, Version: version, Plural: plural, Scope: scope,
		Relation: relation,
	}
}

func kindResourceRef(kind, namespace, name, relation string) (crd.RelatedRef, bool) {
	switch kind {
	case "Deployment":
		return resourceRef(kind, namespace, name, "apps", "v1", "deployments", "Namespaced", relation), true
	case "StatefulSet":
		return resourceRef(kind, namespace, name, "apps", "v1", "statefulsets", "Namespaced", relation), true
	case "DaemonSet":
		return resourceRef(kind, namespace, name, "apps", "v1", "daemonsets", "Namespaced", relation), true
	case "ReplicaSet":
		return resourceRef(kind, namespace, name, "apps", "v1", "replicasets", "Namespaced", relation), true
	case "Job":
		return resourceRef(kind, namespace, name, "batch", "v1", "jobs", "Namespaced", relation), true
	case "CronJob":
		return resourceRef(kind, namespace, name, "batch", "v1", "cronjobs", "Namespaced", relation), true
	case "Service":
		return resourceRef(kind, namespace, name, "", "v1", "services", "Namespaced", relation), true
	case "Pod":
		return resourceRef(kind, namespace, name, "", "v1", "pods", "Namespaced", relation), true
	}
	return crd.RelatedRef{}, false
}

func ownerRefs(namespace string, u *unstructured.Unstructured) []crd.RelatedRef {
	owners := u.GetOwnerReferences()
	out := make([]crd.RelatedRef, 0, len(owners))
	for _, owner := range owners {
		if ref, ok := kindResourceRef(owner.Kind, namespace, owner.Name, "owner"); ok {
			out = append(out, ref)
		}
	}
	return out
}

func (c *ClusterConn) serviceRelatedRefs(ctx context.Context, ns, name string) []crd.RelatedRef {
	if c.typed == nil {
		return nil
	}
	slices, err := c.typed.DiscoveryV1().EndpointSlices(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "kubernetes.io/service-name=" + name,
	})
	if err != nil || slices == nil {
		return nil
	}
	out := make([]crd.RelatedRef, 0, len(slices.Items))
	for i := range slices.Items {
		slice := &slices.Items[i]
		out = append(out, resourceRef("EndpointSlice", ns, slice.Name, "discovery.k8s.io", "v1", "endpointslices", "Namespaced", "backing endpoints"))
		for _, ep := range slice.Endpoints {
			if ep.TargetRef != nil && ep.TargetRef.Kind == "Pod" && ep.TargetRef.Name != "" {
				out = append(out, resourceRef("Pod", ns, ep.TargetRef.Name, "", "v1", "pods", "Namespaced", "endpoint pod"))
			}
		}
	}
	return out
}

func issuerRelatedRef(u *unstructured.Unstructured, namespace, relation string) (crd.RelatedRef, bool) {
	name := nestedString(u.Object, "spec", "issuerRef", "name")
	if name == "" {
		return crd.RelatedRef{}, false
	}
	kind := nestedString(u.Object, "spec", "issuerRef", "kind")
	if kind == "" {
		kind = "Issuer"
	}
	if kind == "ClusterIssuer" {
		return resourceRef("ClusterIssuer", "", name, "cert-manager.io", "v1", "clusterissuers", "Cluster", relation), true
	}
	return resourceRef("Issuer", namespace, name, "cert-manager.io", "v1", "issuers", "Namespaced", relation), true
}

func ingressServiceRefs(u *unstructured.Unstructured, namespace string) []crd.RelatedRef {
	names := map[string]struct{}{}
	addBackend := func(backend map[string]interface{}) {
		name := nestedString(backend, "service", "name")
		if name != "" {
			names[name] = struct{}{}
		}
	}
	if b, ok, _ := unstructured.NestedMap(u.Object, "spec", "defaultBackend"); ok {
		addBackend(b)
	}
	rules, _, _ := unstructured.NestedSlice(u.Object, "spec", "rules")
	for _, rawRule := range rules {
		rule, ok := rawRule.(map[string]interface{})
		if !ok {
			continue
		}
		paths, _, _ := unstructured.NestedSlice(rule, "http", "paths")
		for _, rawPath := range paths {
			path, ok := rawPath.(map[string]interface{})
			if !ok {
				continue
			}
			if b, ok, _ := unstructured.NestedMap(path, "backend"); ok {
				addBackend(b)
			}
		}
	}
	out := make([]crd.RelatedRef, 0, len(names))
	for svc := range names {
		out = append(out, resourceRef("Service", namespace, svc, "", "v1", "services", "Namespaced", "backend service"))
	}
	return out
}

// RevealSecretKey fetches the decoded value of one key from a Secret.
// secret.Data is already []byte (client-go decodes base64 transparently).
// Returns an error for missing key or missing secret. The value is returned
// as a string and is NEVER logged.
func (c *ClusterConn) RevealSecretKey(ctx context.Context, ns, name, key string) (string, error) {
	secret, err := c.typed.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	b, ok := secret.Data[key]
	if !ok {
		return "", fmt.Errorf("key %q not found in secret %s/%s", key, ns, name)
	}
	return string(b), nil
}

// buildServiceBacking fetches the typed Service and its EndpointSlices, then
// builds a ServiceBacking. Non-fatal: any error returns an empty backing so the
// detail still renders cleanly.
func (c *ClusterConn) buildServiceBacking(ctx context.Context, ns, name string) crd.ServiceBacking {
	svc, err := c.typed.CoreV1().Services(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return crd.ServiceBacking{}
	}
	sliceList, err := c.typed.DiscoveryV1().EndpointSlices(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "kubernetes.io/service-name=" + name,
	})
	if err != nil || sliceList == nil {
		// No EndpointSlices found; build backing from Service spec only.
		return crd.BuildServiceBacking(svc, nil)
	}
	return crd.BuildServiceBacking(svc, sliceList.Items)
}

// instanceEvents lists core Events for an object's uid, newest first. A list
// error degrades to no events (the detail still renders).
func (c *ClusterConn) instanceEvents(ctx context.Context, uid string) []crd.Event {
	if uid == "" {
		return nil
	}
	sel := fields.OneTermEqualSelector("involvedObject.uid", uid).String()
	list, err := c.typed.CoreV1().Events("").List(ctx, metav1.ListOptions{FieldSelector: sel, Limit: 50})
	if err != nil || list == nil {
		return nil
	}
	out := make([]crd.Event, 0, len(list.Items))
	for i := range list.Items {
		e := &list.Items[i]
		last := e.LastTimestamp.Time
		if last.IsZero() {
			last = e.EventTime.Time
		}
		// coverage S0b §3: a controller can echo a credential-bearing URL
		out = append(out, crd.Event{Type: e.Type, Reason: e.Reason, Message: urlscrub.Text(e.Message).Text, Count: e.Count, Last: last})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Last.After(out[b].Last) })
	return out
}
