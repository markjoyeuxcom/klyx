package appbridge

import (
	"regexp"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/admission"
	"github.com/moomora/klyx/internal/ai"
	"github.com/moomora/klyx/internal/crd"
)

// The chat projection (spec 2026-10-14 §2): the one contract for what an
// object can send, shared by tools and snapshots. It is built from the live
// unstructured object, never from the inspector's YAML. Exclusions are
// structural - a field outside the per-kind allowlist is not copied - and
// every string that is copied passes the scrubber and a cap.

// ChatObject is one projected object.
type ChatObject struct {
	Kind               string            `json:"kind"`
	GVR                string            `json:"gvr"`
	Namespace          string            `json:"namespace,omitempty"`
	Name               string            `json:"name"`
	UID                string            `json:"uid"`
	Generation         int64             `json:"generation,omitempty"`
	Created            string            `json:"created,omitempty"`
	Owners             []string          `json:"owners,omitempty"`
	Labels             map[string]string `json:"labels,omitempty"`
	Annotations        map[string]string `json:"annotations,omitempty"`
	AnnotationsDropped int               `json:"annotationsDropped,omitempty"`
	Conditions         []chatCondition   `json:"conditions,omitempty"`
	Facts              []chatFact        `json:"facts,omitempty"`
	Spec               any               `json:"spec,omitempty"`
	Status             any               `json:"status,omitempty"`
	FieldsDropped      []string          `json:"fieldsDropped,omitempty"`
	Redacted           int               `json:"-"`
}

type chatCondition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
	Scope   string `json:"scope,omitempty"`
}

type chatFact struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// chatAnnotationAllowlist is every annotation chat may send; all others are
// dropped and counted.
var chatAnnotationAllowlist = map[string]bool{
	"deployment.kubernetes.io/revision":     true,
	"argocd.argoproj.io/sync-wave":          true,
	"argocd.argoproj.io/sync-options":       true,
	"kustomize.toolkit.fluxcd.io/reconcile": true,
	"helm.sh/resource-policy":               true,
}

// chatDropKeys are subtrees never copied from any spec or status, whatever
// the kind: Helm values in every spelling and raw data maps.
var chatDropKeys = map[string]bool{
	"values": true, "valuesObject": true, "data": true, "stringData": true, "binaryData": true,
}

// chatSensitiveKey matches key names whose values are credentials or keys.
// A key that names a reference (…Name, …Ref, …Refs, …Selector, …Namespace)
// is exempt: it names another object, it is not a secret value.
var chatSensitiveKey = regexp.MustCompile(`(?i)(passw|passwd|pwd|token|secret|api[_-]?key|access[_-]?key|credential|auth|cert|private|signature|^key$|[_-]key$|Key$)`)
var chatReferenceKey = regexp.MustCompile(`(Name|Ref|Refs|Selector|Namespace|Names)$`)

func chatSensitive(key string) bool {
	return chatSensitiveKey.MatchString(key) && !chatReferenceKey.MatchString(key)
}

const (
	chatStringMax    = 500 // a free-text value
	chatCRDStringMax = 200 // a string leaf of an unknown kind's spec
	chatListMax      = 50  // list items kept per array
	chatTreeDepth    = 6   // nesting kept for known kinds
	chatCRDDepth     = 4   // nesting kept for unknown kinds (spec §2)
)

// projector carries the redaction count for one projection.
type projector struct {
	redacted int
	dropped  map[string]bool
}

func (p *projector) str(s string, max int) string {
	out, n := ai.ScrubEvidence(s, max)
	p.redacted += n
	return out
}

func (p *projector) drop(path string) {
	if p.dropped == nil {
		p.dropped = map[string]bool{}
	}
	p.dropped[path] = true
}

// tree copies v with the structural rules: drop keys never cross, sensitive
// keys keep only their shape ("[masked]" for scalars), strings are scrubbed
// and capped, arrays and depth are bounded. scalarsOnly keeps only scalar
// leaves (an unknown kind's spec).
func (p *projector) tree(v any, path string, depth, maxDepth, strMax int, scalarsOnly bool) any {
	switch t := v.(type) {
	case map[string]any:
		if depth >= maxDepth {
			p.drop(path + " (deeper fields)")
			return nil
		}
		out := map[string]any{}
		for k, child := range t {
			cp := path + "." + k
			if chatDropKeys[k] {
				p.drop(cp)
				continue
			}
			if chatSensitive(k) {
				switch child.(type) {
				case map[string]any, []any:
					p.drop(cp)
				default:
					out[k] = "[masked]"
					p.redacted++
				}
				continue
			}
			if x := p.tree(child, cp, depth+1, maxDepth, strMax, scalarsOnly); x != nil {
				out[k] = x
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case []any:
		var out []any
		for i, child := range t {
			if i >= chatListMax {
				p.drop(path + " (later items)")
				break
			}
			if x := p.tree(child, path+"[]", depth+1, maxDepth, strMax, scalarsOnly); x != nil {
				out = append(out, x)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case string:
		return p.str(t, strMax)
	case bool, int64, float64, int, int32:
		return t
	case nil:
		return nil
	default:
		return nil
	}
}

// chatProject projects one live object. gvr names the served resource.
func chatProject(gvr schema.GroupVersionResource, u *unstructured.Unstructured) ChatObject {
	p := &projector{}
	o := ChatObject{
		Kind:       u.GetKind(),
		GVR:        gvrString(gvr),
		Namespace:  u.GetNamespace(),
		Name:       u.GetName(),
		UID:        string(u.GetUID()),
		Generation: u.GetGeneration(),
	}
	if ts := u.GetCreationTimestamp(); !ts.IsZero() {
		o.Created = ts.UTC().Format("2006-01-02T15:04:05Z")
	}
	for _, ref := range u.GetOwnerReferences() {
		o.Owners = append(o.Owners, ref.Kind+"/"+ref.Name)
	}
	if l := u.GetLabels(); len(l) > 0 {
		o.Labels = map[string]string{}
		for k, v := range l {
			o.Labels[k] = p.str(v, chatStringMax)
		}
	}
	for k, v := range u.GetAnnotations() {
		if chatAnnotationAllowlist[k] {
			if o.Annotations == nil {
				o.Annotations = map[string]string{}
			}
			o.Annotations[k] = p.str(v, chatStringMax)
		} else {
			o.AnnotationsDropped++
		}
	}
	for _, c := range crd.ParseConditionsDeep(gvr.Group, gvr.Resource, u.Object) {
		o.Conditions = append(o.Conditions, chatCondition{
			Type: c.Type, Status: c.Status, Reason: p.str(c.Reason, chatStringMax), Message: p.str(c.Message, chatStringMax), Scope: c.Scope,
		})
	}
	o.Spec, o.Status = p.kindSpec(gvr, u)
	if gvr.Group != "" || (gvr.Resource != "secrets" && gvr.Resource != "configmaps") {
		// Facts come from a structurally sanitized copy: some builders copy
		// ownership annotations into values (Codex re-review of f012dc1).
		for _, f := range crd.BuildFacts(gvr.Group, gvr.Resource, factsInput(u)) {
			o.Facts = append(o.Facts, chatFact{Label: strings.TrimSpace(f.Group + " " + f.Label), Value: p.str(f.Value, chatStringMax)})
		}
	}
	for k := range p.dropped {
		o.FieldsDropped = append(o.FieldsDropped, strings.TrimPrefix(k, "."))
	}
	sort.Strings(o.FieldsDropped)
	o.Redacted = p.redacted
	return o
}

// factsInput is the copy fact builders see: only allowlisted annotations,
// and no data maps or Helm values.
func factsInput(u *unstructured.Unstructured) *unstructured.Unstructured {
	c := u.DeepCopy()
	keep := map[string]string{}
	for k, v := range c.GetAnnotations() {
		if chatAnnotationAllowlist[k] {
			keep[k] = v
		}
	}
	c.SetAnnotations(keep)
	for _, k := range []string{"data", "stringData", "binaryData"} {
		delete(c.Object, k)
	}
	if spec, ok := c.Object["spec"].(map[string]any); ok {
		delete(spec, "values")
		delete(spec, "valuesObject")
	}
	return c
}

func gvrString(gvr schema.GroupVersionResource) string {
	if gvr.Group == "" {
		return gvr.Version + "/" + gvr.Resource
	}
	return gvr.Group + "/" + gvr.Version + "/" + gvr.Resource
}

// kindSpec is the per-kind spec and status projection (spec §2 table).
func (p *projector) kindSpec(gvr schema.GroupVersionResource, u *unstructured.Unstructured) (spec, status any) {
	obj := u.Object
	key := gvr.Group + "/" + gvr.Resource
	switch key {
	case "/secrets":
		out := map[string]any{"type": p.str(nestedString(obj, "type"), chatStringMax)}
		out["keys"] = mapKeys(obj, "data", "stringData")
		p.drop(".data")
		return out, nil
	case "/configmaps":
		// Key names and sizes only: a ConfigMap can carry Helm values
		// through valuesFrom under any key, so no value is classified safe.
		out := map[string]any{}
		data, _, _ := unstructured.NestedStringMap(obj, "data")
		sizes := map[string]any{}
		for k, v := range data {
			sizes[k] = len(v)
		}
		if len(sizes) > 0 {
			out["dataKeyBytes"] = sizes
			p.drop(".data")
		}
		if bk := mapKeys(obj, "binaryData"); len(bk) > 0 {
			out["binaryDataKeys"] = bk
			p.drop(".binaryData")
		}
		return out, nil
	case "/pods":
		return p.podSpec(nestedMap(obj, "spec")), p.podStatus(nestedMap(obj, "status"))
	case "apps/deployments", "apps/statefulsets", "apps/daemonsets", "apps/replicasets":
		return p.workloadSpec(nestedMap(obj, "spec"), "template"), p.tree(nestedMap(obj, "status"), ".status", 0, chatTreeDepth, chatStringMax, false)
	case "batch/jobs":
		return p.workloadSpec(nestedMap(obj, "spec"), "template"), p.tree(nestedMap(obj, "status"), ".status", 0, chatTreeDepth, chatStringMax, false)
	case "batch/cronjobs":
		spec := nestedMap(obj, "spec")
		out := p.pick(spec, "schedule", "suspend", "concurrencyPolicy", "successfulJobsHistoryLimit", "failedJobsHistoryLimit", "startingDeadlineSeconds", "timeZone")
		if jt := nestedMap(spec, "jobTemplate", "spec"); jt != nil {
			out["jobTemplate"] = p.workloadSpec(jt, "template")
		}
		return out, p.tree(nestedMap(obj, "status"), ".status", 0, chatTreeDepth, chatStringMax, false)
	case "helm.toolkit.fluxcd.io/helmreleases":
		spec := nestedMap(obj, "spec")
		out := p.pick(spec, "interval", "releaseName", "targetNamespace", "suspend", "timeout", "install", "upgrade", "rollback", "uninstall", "dependsOn", "chartRef")
		if ch := nestedMap(spec, "chart"); ch != nil {
			out["chart"] = p.tree(ch, ".spec.chart", 0, chatTreeDepth, chatStringMax, false)
		}
		if vf, ok := spec["valuesFrom"].([]any); ok {
			var refs []any
			for _, x := range vf {
				if m, ok := x.(map[string]any); ok {
					refs = append(refs, map[string]any{"kind": m["kind"], "name": m["name"]})
				}
			}
			out["valuesFrom"] = refs
		}
		p.drop(".spec.values")
		p.drop(".spec.postRenderers")
		return out, p.tree(nestedMap(obj, "status"), ".status", 0, chatTreeDepth, chatStringMax, false)
	case "argoproj.io/applications":
		return p.argoAppSpec(nestedMap(obj, "spec")), p.tree(nestedMap(obj, "status"), ".status", 0, chatTreeDepth, chatStringMax, false)
	case "argoproj.io/applicationsets":
		spec := nestedMap(obj, "spec")
		out := p.pick(spec, "syncPolicy", "goTemplate")
		if gens, ok := spec["generators"].([]any); ok {
			// Generator kinds only: list elements, plugin parameters and
			// matrix inputs are arbitrary values (Codex review of aea77e7).
			var kinds []string
			for _, g := range gens {
				if gm, ok := g.(map[string]any); ok {
					for k := range gm {
						kinds = append(kinds, k)
					}
				}
			}
			sort.Strings(kinds)
			out["generators"] = kinds
			p.drop(".spec.generators (values)")
		}
		if tmpl := nestedMap(spec, "template", "spec"); tmpl != nil {
			out["template"] = p.argoAppSpec(tmpl)
		}
		return out, p.tree(nestedMap(obj, "status"), ".status", 0, chatTreeDepth, chatStringMax, false)
	case "/services", "networking.k8s.io/ingresses", "networking.k8s.io/networkpolicies",
		"autoscaling/horizontalpodautoscalers", "policy/poddisruptionbudgets", "/nodes",
		"gateway.networking.k8s.io/gateways", "gateway.networking.k8s.io/httproutes", "gateway.networking.k8s.io/grpcroutes",
		"gateway.networking.k8s.io/tcproutes", "gateway.networking.k8s.io/tlsroutes", "gateway.networking.k8s.io/gatewayclasses",
		"/namespaces", "/persistentvolumeclaims", "/persistentvolumes", "storage.k8s.io/storageclasses":
		return p.tree(nestedMap(obj, "spec"), ".spec", 0, chatTreeDepth, chatStringMax, false),
			p.tree(nestedMap(obj, "status"), ".status", 0, chatTreeDepth, chatStringMax, false)
	}
	switch key {
	case "admissionregistration.k8s.io/validatingadmissionpolicies", "admissionregistration.k8s.io/mutatingadmissionpolicies":
		// Coverage S3a §8: counts and presence, never an expression or a
		// type-checking warning's text (it quotes expression source)
		pol := admission.ProjectPolicy(u, admission.KindOf(gvr.Resource))
		out := map[string]any{"failurePolicy": pol.FailurePolicy, "validations": pol.Validations, "mutations": pol.Mutations,
			"variables": pol.Variables, "matchConditions": pol.MatchConditions, "auditAnnotations": pol.AuditAnnotations,
			"matchRules": pol.MatchRuleCount}
		if pol.ParamKind != nil {
			out["paramKind"] = map[string]any{"apiVersion": pol.ParamKind.APIVersion, "kind": pol.ParamKind.Kind}
		}
		p.drop(".spec (expressions)")
		var st map[string]any
		if pol.HasStatus {
			st = map[string]any{"observedGeneration": pol.ObservedGeneration, "typeCheckingWarnings": pol.WarningCount, "warningFields": pol.WarningFields}
			p.drop(".status.typeChecking (warning text)")
		}
		return out, st
	case "admissionregistration.k8s.io/validatingadmissionpolicybindings", "admissionregistration.k8s.io/mutatingadmissionpolicybindings":
		b := admission.ProjectBinding(u, admission.KindOf(gvr.Resource))
		out := map[string]any{"policyName": b.PolicyName, "validationActions": b.Actions, "matchRules": b.MatchRules}
		if b.ParamRef != nil {
			out["paramRef"] = map[string]any{"name": b.ParamRef.Name, "namespace": b.ParamRef.Namespace, "selectorSet": b.ParamRef.Selector,
				"parameterNotFoundAction": b.ParamRef.NotFoundAction}
		}
		p.drop(".spec (selector values)")
		return out, nil
	}
	if key == "image.toolkit.fluxcd.io/imageupdateautomations" {
		// Coverage S1b §6: the author's email and the policy selector are
		// never AI facts; the rest is any CRD's projection.
		spec := deepCopyMap(nestedMap(obj, "spec"))
		if a := nestedMap(spec, "git", "commit", "author"); a != nil {
			if _, ok := a["email"]; ok {
				delete(a, "email")
				p.drop(".spec.git.commit.author.email")
			}
		}
		if _, ok := spec["policySelector"]; ok {
			delete(spec, "policySelector")
			p.drop(".spec.policySelector")
		}
		return p.tree(spec, ".spec", 0, chatCRDDepth, chatCRDStringMax, true),
			p.tree(nestedMap(obj, "status"), ".status", 0, chatCRDDepth, chatCRDStringMax, true)
	}
	// Any other kind (CRDs): scalar leaves to depth 4, strings key-aware
	// masked, scrubbed and capped at 200 characters (spec §2).
	return p.tree(nestedMap(obj, "spec"), ".spec", 0, chatCRDDepth, chatCRDStringMax, true),
		p.tree(nestedMap(obj, "status"), ".status", 0, chatCRDDepth, chatCRDStringMax, true)
}

// pick copies only the named members of m, each through tree.
func (p *projector) pick(m map[string]any, keys ...string) map[string]any {
	out := map[string]any{}
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if x := p.tree(v, ".spec."+k, 0, chatTreeDepth, chatStringMax, false); x != nil {
				out[k] = x
			}
		}
	}
	return out
}

// workloadSpec projects a workload: replicas, selector, strategy and the pod
// template (or the pod spec at templateKey's spec).
func (p *projector) workloadSpec(spec map[string]any, templateKey string) map[string]any {
	if spec == nil {
		return nil
	}
	out := p.pick(spec, "replicas", "selector", "strategy", "updateStrategy", "minReadySeconds", "revisionHistoryLimit",
		"paused", "progressDeadlineSeconds", "serviceName", "podManagementPolicy", "parallelism", "completions",
		"backoffLimit", "activeDeadlineSeconds", "suspend")
	if ps := nestedMap(spec, templateKey, "spec"); ps != nil {
		out["template"] = p.podSpec(ps)
	}
	return out
}

// podSpec keeps scheduling and container shape; env values, command, args and
// probe header values never cross (spec §2).
func (p *projector) podSpec(spec map[string]any) map[string]any {
	if spec == nil {
		return nil
	}
	out := p.pick(spec, "nodeName", "nodeSelector", "tolerations", "serviceAccountName", "priorityClassName",
		"restartPolicy", "schedulerName", "hostNetwork", "dnsPolicy", "runtimeClassName", "terminationGracePeriodSeconds")
	if aff := nestedMap(spec, "affinity"); aff != nil {
		var kinds []string
		for k := range aff {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		out["affinity"] = kinds
	}
	for _, key := range []string{"initContainers", "containers"} {
		list, _ := spec[key].([]any)
		var cs []any
		for _, x := range list {
			m, ok := x.(map[string]any)
			if !ok {
				continue
			}
			cs = append(cs, p.container(m))
		}
		if len(cs) > 0 {
			out[key] = cs
		}
	}
	if vols, ok := spec["volumes"].([]any); ok {
		var vs []any
		for _, x := range vols {
			m, ok := x.(map[string]any)
			if !ok {
				continue
			}
			v := map[string]any{"name": m["name"]}
			for k, src := range m {
				if k == "name" {
					continue
				}
				v["type"] = k
				if sm, ok := src.(map[string]any); ok {
					for _, rk := range []string{"name", "secretName", "claimName"} {
						if s, ok := sm[rk].(string); ok {
							v["ref"] = p.str(s, chatStringMax)
						}
					}
				}
			}
			vs = append(vs, v)
		}
		out["volumes"] = vs
	}
	p.drop(".spec.containers[].env[].value")
	p.drop(".spec.containers[].command")
	p.drop(".spec.containers[].args")
	return out
}

func (p *projector) container(m map[string]any) map[string]any {
	c := map[string]any{}
	for _, k := range []string{"name", "image", "imagePullPolicy"} {
		if s, ok := m[k].(string); ok {
			c[k] = p.str(s, chatStringMax)
		}
	}
	if ports, ok := m["ports"]; ok {
		c["ports"] = p.tree(ports, ".ports", 0, 3, chatStringMax, false)
	}
	if res := nestedMap(m, "resources"); res != nil {
		c["resources"] = p.tree(res, ".resources", 0, 3, chatStringMax, false)
	}
	for _, probe := range []string{"livenessProbe", "readinessProbe", "startupProbe"} {
		pm := nestedMap(m, probe)
		if pm == nil {
			continue
		}
		pr := map[string]any{}
		for _, t := range []string{"httpGet", "tcpSocket", "exec", "grpc"} {
			if h := nestedMap(pm, t); h != nil {
				pr["type"] = t
				if port, ok := h["port"]; ok {
					pr["port"] = port
				}
				if path, ok := h["path"].(string); ok {
					pr["path"] = p.str(path, chatStringMax)
				}
			}
		}
		for _, k := range []string{"initialDelaySeconds", "periodSeconds", "timeoutSeconds", "failureThreshold", "successThreshold"} {
			if v, ok := pm[k]; ok {
				pr[k] = v
			}
		}
		c[probe] = pr
	}
	if env, ok := m["env"].([]any); ok {
		var names []any
		for _, x := range env {
			e, ok := x.(map[string]any)
			if !ok {
				continue
			}
			item := map[string]any{"name": e["name"]}
			if vf := nestedMap(e, "valueFrom"); vf != nil {
				for src, ref := range vf {
					rm, _ := ref.(map[string]any)
					item["from"] = src
					if n, ok := rm["name"].(string); ok {
						item["ref"] = p.str(n, chatStringMax)
					}
					if fp, ok := rm["fieldPath"].(string); ok {
						item["ref"] = fp
					}
				}
			}
			names = append(names, item)
		}
		c["env"] = names
	}
	if ef, ok := m["envFrom"].([]any); ok {
		var refs []any
		for _, x := range ef {
			e, ok := x.(map[string]any)
			if !ok {
				continue
			}
			for src, ref := range e {
				if rm, ok := ref.(map[string]any); ok {
					refs = append(refs, map[string]any{"from": src, "ref": rm["name"]})
				}
			}
		}
		c["envFrom"] = refs
	}
	if vm, ok := m["volumeMounts"]; ok {
		c["volumeMounts"] = p.tree(vm, ".volumeMounts", 0, 3, chatStringMax, false)
	}
	return c
}

// podStatus keeps phase, reasons and per-container state; messages are
// scrubbed.
func (p *projector) podStatus(st map[string]any) any {
	if st == nil {
		return nil
	}
	out := p.pick(st, "phase", "reason", "message", "qosClass", "startTime", "hostIP", "podIP")
	for _, key := range []string{"initContainerStatuses", "containerStatuses"} {
		if v, ok := st[key]; ok {
			out[key] = p.tree(v, ".status."+key, 0, 5, chatStringMax, false)
		}
	}
	return out
}

// argoAppSpec projects an Argo Application spec: source and destination,
// sync policy and Helm parameter NAMES; values, valuesObject, parameter
// values and plugin env values never cross.
func (p *projector) argoAppSpec(spec map[string]any) map[string]any {
	if spec == nil {
		return nil
	}
	out := p.pick(spec, "project", "destination", "syncPolicy", "revisionHistoryLimit")
	src := func(s map[string]any) map[string]any {
		o := map[string]any{}
		for _, k := range []string{"repoURL", "path", "targetRevision", "chart", "ref"} {
			if v, ok := s[k].(string); ok {
				o[k] = p.str(v, chatStringMax)
			}
		}
		if h := nestedMap(s, "helm"); h != nil {
			hm := map[string]any{}
			if v, ok := h["releaseName"].(string); ok {
				hm["releaseName"] = p.str(v, chatStringMax)
			}
			if vf, ok := h["valueFiles"]; ok {
				hm["valueFiles"] = p.tree(vf, ".helm.valueFiles", 0, 2, chatStringMax, false)
			}
			if params, ok := h["parameters"].([]any); ok {
				var names []any
				for _, x := range params {
					if pm, ok := x.(map[string]any); ok {
						names = append(names, pm["name"])
					}
				}
				hm["parameterNames"] = names
			}
			p.drop(".spec.source.helm.values")
			p.drop(".spec.source.helm.valuesObject")
			o["helm"] = hm
		}
		if k := nestedMap(s, "kustomize"); k != nil {
			o["kustomize"] = p.kustomize(k)
		}
		if pl := nestedMap(s, "plugin"); pl != nil {
			if n, ok := pl["name"].(string); ok {
				o["plugin"] = p.str(n, chatStringMax)
			}
			p.drop(".spec.source.plugin.env")
		}
		return o
	}
	if s := nestedMap(spec, "source"); s != nil {
		out["source"] = src(s)
	}
	if list, ok := spec["sources"].([]any); ok {
		var srcs []any
		for _, x := range list {
			if m, ok := x.(map[string]any); ok {
				srcs = append(srcs, src(m))
			}
		}
		out["sources"] = srcs
	}
	return out
}

// kustomize is an Argo source's Kustomize block by explicit allowlist:
// patches and inline manifests are opaque payloads (a ConfigMap patch can
// carry any value) and never cross; commonAnnotations pass the annotation
// allowlist (Codex review of aea77e7).
func (p *projector) kustomize(k map[string]any) map[string]any {
	out := map[string]any{}
	for _, key := range []string{"namePrefix", "nameSuffix", "namespace", "version", "forceCommonLabels", "forceCommonAnnotations"} {
		if v, ok := k[key]; ok {
			if x := p.tree(v, ".kustomize."+key, 0, 1, chatStringMax, false); x != nil {
				out[key] = x
			}
		}
	}
	if imgs, ok := k["images"].([]any); ok {
		var list []any
		for _, i := range imgs {
			if s, ok := i.(string); ok {
				list = append(list, p.str(s, chatStringMax))
			}
		}
		out["images"] = list
	}
	if comps, ok := k["components"].([]any); ok {
		var list []any
		for _, c := range comps {
			if s, ok := c.(string); ok {
				list = append(list, p.str(s, chatStringMax))
			}
		}
		out["components"] = list
	}
	if reps, ok := k["replicas"].([]any); ok {
		var list []any
		for _, r := range reps {
			if rm, ok := r.(map[string]any); ok {
				name, _ := rm["name"].(string)
				list = append(list, map[string]any{"name": p.str(name, chatStringMax), "count": rm["count"]})
			}
		}
		out["replicas"] = list
	}
	if labels, ok := k["commonLabels"].(map[string]any); ok {
		lm := map[string]any{}
		for lk, lv := range labels {
			if s, ok := lv.(string); ok {
				lm[lk] = p.str(s, chatStringMax)
			}
		}
		out["commonLabels"] = lm
	}
	if ann, ok := k["commonAnnotations"].(map[string]any); ok {
		am := map[string]any{}
		for ak, av := range ann {
			if s, ok := av.(string); ok && chatAnnotationAllowlist[ak] {
				am[ak] = p.str(s, chatStringMax)
			} else {
				p.drop(".kustomize.commonAnnotations")
			}
		}
		if len(am) > 0 {
			out["commonAnnotations"] = am
		}
	}
	for _, opaque := range []string{"patches", "patchesStrategicMerge", "patchesJson6902"} {
		if v, ok := k[opaque].([]any); ok && len(v) > 0 {
			out[opaque+"Count"] = len(v)
			p.drop(".kustomize." + opaque)
		}
	}
	return out
}

func nestedMap(m map[string]any, path ...string) map[string]any {
	if m == nil {
		return nil
	}
	v, ok, _ := unstructured.NestedMap(m, path...)
	if !ok {
		return nil
	}
	return v
}

func nestedString(m map[string]any, path ...string) string {
	s, _, _ := unstructured.NestedString(m, path...)
	return s
}

func mapKeys(m map[string]any, fields ...string) []string {
	var keys []string
	for _, f := range fields {
		if d, ok := m[f].(map[string]any); ok {
			for k := range d {
				keys = append(keys, k)
			}
		}
	}
	sort.Strings(keys)
	return keys
}

// deepCopyMap copies a JSON object, so a projection can drop members
// without touching the object it was given.
func deepCopyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	return runtime.DeepCopyJSONValue(m).(map[string]any)
}
