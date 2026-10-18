package identity

import (
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ESO store kinds.
const (
	KindSecretStore        = "SecretStore"
	KindClusterSecretStore = "ClusterSecretStore"
)

// Read is how a lookup went.
type Read int

const (
	ReadFound Read = iota
	ReadMissing
	ReadUnreadable
)

// SARef is a store's serviceAccountRef, resolved per ESO's namespace rule.
type SARef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	// Referent: a ClusterSecretStore without a namespace resolves per
	// ExternalSecret, to each one's own namespace; no one ServiceAccount.
	Referent bool `json:"referent,omitempty"`
}

// Subject is who a store authenticates as (S5 §4.1): its base credential
// and any requested principal on top, with ESO's deterministic refusals
// and what Klyx cannot resolve.
type Subject struct {
	Provider string `json:"provider"`
	Mode     string `json:"mode"`
	Base     string `json:"base"`
	SA       *SARef `json:"serviceAccount,omitempty"`
	// Controller: the base is the handling controller's own identity (4.2).
	Controller bool `json:"controller,omitempty"`
	// ControllerEnv: Azure's branch that reads the controller's own env.
	ControllerEnv bool `json:"controllerEnv,omitempty"`
	// Requested is the chain on top of the base, in order (AWS: each of
	// additionalRoles, then role; GCP: the impersonated GSA).
	Requested  []string `json:"requested,omitempty"`
	Refusals   []string `json:"refusals,omitempty"`
	Unresolved []string `json:"unresolved,omitempty"`
	Notes      []string `json:"notes,omitempty"`
}

// SALookup reads a ServiceAccount by namespace and name.
type SALookup func(ns, name string) (*corev1.ServiceAccount, Read)

// StoreClass is the store's spec.controller ("" accepts every controller).
func StoreClass(st *unstructured.Unstructured) string {
	s, _, _ := unstructured.NestedString(st.Object, "spec", "controller")
	return s
}

func strField(m map[string]any, keys ...string) (string, bool) {
	s, ok, _ := unstructured.NestedString(m, keys...)
	return s, ok
}

func mapField(m map[string]any, keys ...string) map[string]any {
	v, ok, _ := unstructured.NestedMap(m, keys...)
	if !ok {
		return nil
	}
	return v
}

// saRef resolves a serviceAccountRef: ESO's rule for a namespaced store
// (empty or its own namespace; else ESO refuses), and the referent rule
// for a ClusterSecretStore.
func saRef(ref map[string]any, kind, storeNS string, sub *Subject) *SARef {
	if ref == nil {
		return nil
	}
	name, _ := strField(ref, "name")
	ns, hasNS := strField(ref, "namespace")
	r := &SARef{Name: clipName(name)}
	switch {
	case kind == KindSecretStore:
		if hasNS && ns != "" && ns != storeNS {
			sub.Refusals = append(sub.Refusals, "ESO refuses this store: serviceAccountRef.namespace must be empty or the store's own namespace")
		}
		r.Namespace = storeNS
	case hasNS && ns != "":
		r.Namespace = clipName(ns)
	default:
		r.Referent = true
	}
	return r
}

func clipName(s string) string {
	if len(s) > 253 {
		return s[:253]
	}
	return s
}

// lookupSA reads the referenced ServiceAccount, or says why not.
func lookupSA(r *SARef, get SALookup, sub *Subject) (*corev1.ServiceAccount, bool) {
	if r == nil || r.Referent || get == nil {
		if r != nil && r.Referent {
			sub.Notes = append(sub.Notes, "a ClusterSecretStore without a namespace authenticates as the named ServiceAccount in each ExternalSecret's own namespace")
		}
		return nil, false
	}
	sa, read := get(r.Namespace, r.Name)
	switch read {
	case ReadMissing:
		sub.Unresolved = append(sub.Unresolved, "the ServiceAccount "+r.Namespace+"/"+r.Name+" does not exist")
		return nil, false
	case ReadUnreadable:
		sub.Unresolved = append(sub.Unresolved, "the ServiceAccount "+r.Namespace+"/"+r.Name+" could not be read")
		return nil, false
	}
	return sa, true
}

// StoreSubject resolves who a store authenticates as, per provider and
// mode (S5 §4.1; ESO v2.13.0).
func StoreSubject(st *unstructured.Unstructured, get SALookup) Subject {
	kind, ns := st.GetKind(), st.GetNamespace()
	prov := mapField(st.Object, "spec", "provider")
	switch {
	case prov["aws"] != nil:
		return awsSubject(mapField(prov, "aws"), kind, ns, get)
	case prov["azurekv"] != nil:
		return azureSubject(mapField(prov, "azurekv"), kind, ns, get)
	case prov["gcpsm"] != nil:
		return gcpSubject(mapField(prov, "gcpsm"), kind, ns, get)
	}
	names := make([]string, 0, len(prov))
	for k := range prov {
		names = append(names, k)
	}
	sort.Strings(names)
	return Subject{Provider: strings.Join(names, ","), Mode: "other", Base: "not read by Klyx's identity chain"}
}

func roleShown(s string) string { return valueOf(s, shapeARN).Shown }

func awsSubject(p map[string]any, kind, ns string, get SALookup) Subject {
	sub := Subject{Provider: "aws"}
	auth := mapField(p, "auth")
	switch {
	case mapField(auth, "jwt", "serviceAccountRef") != nil:
		sub.Mode = "jwt"
		sub.SA = saRef(mapField(auth, "jwt", "serviceAccountRef"), kind, ns, &sub)
		sub.Base = "the ServiceAccount's token, minted by ESO itself (EKS Pod Identity cannot apply)"
		if sa, ok := lookupSA(sub.SA, get, &sub); ok {
			if r, has := sa.Annotations[AnnRoleARN]; has {
				sub.Requested = append(sub.Requested, roleShown(r))
			} else {
				sub.Refusals = append(sub.Refusals, `ESO refuses this store: the ServiceAccount has no `+AnnRoleARN+` ("an IAM role must be associated")`)
			}
		}
	case mapField(auth, "secretRef") != nil:
		sub.Mode, sub.Base = "secretRef", "static keys from a Secret (never read)"
	default:
		sub.Mode, sub.Base, sub.Controller = "default", "the handling controller's credential chain: a configured source, not a proven principal", true
	}
	// each of additionalRoles in order, then role (ESO aws/auth/auth.go 110-130)
	if roles, ok, _ := unstructured.NestedStringSlice(p, "additionalRoles"); ok {
		for _, r := range roles {
			sub.Requested = append(sub.Requested, roleShown(r))
		}
	}
	if r, ok := strField(p, "role"); ok && r != "" {
		sub.Requested = append(sub.Requested, roleShown(r))
	}
	return sub
}

func azureSubject(p map[string]any, kind, ns string, get SALookup) Subject {
	sub := Subject{Provider: "azurekv"}
	authType, _ := strField(p, "authType")
	if authType == "" {
		authType = "ServicePrincipal"
	}
	sub.Mode = authType
	if authType != "WorkloadIdentity" {
		sub.Base = authType + ": named, not followed"
		return sub
	}
	ref := mapField(p, "serviceAccountRef")
	newSDK, _, _ := unstructured.NestedBool(p, "useAzureSDK")
	if ref == nil {
		sub.Base, sub.Controller, sub.ControllerEnv = "the handling controller's own environment (AZURE_CLIENT_ID, AZURE_TENANT_ID, AZURE_FEDERATED_TOKEN_FILE)", true, true
		return sub
	}
	sub.SA = saRef(ref, kind, ns, &sub)
	sub.Base = "the ServiceAccount's token, minted by ESO itself"
	sa, ok := lookupSA(sub.SA, get, &sub)
	tenantStore, storeTenantSet := strField(p, "tenantId")
	var ann map[string]string
	if ok {
		ann = sa.Annotations
	}
	_, clientAnn := ann[AnnAzureClient]
	_, tenantAnn := ann[AnnAzureTenant]
	if newSDK {
		sub.Mode += " (new SDK)"
		if ok && !clientAnn {
			sub.Refusals = append(sub.Refusals, "ESO refuses this store: the ServiceAccount has no "+AnnAzureClient)
		}
		switch {
		case storeTenantSet && tenantStore == "":
			sub.Notes = append(sub.Notes, "the store's empty tenantId is used as is; the ServiceAccount's tenant annotation is not consulted")
		case storeTenantSet:
			sub.Notes = append(sub.Notes, "tenant from the store's tenantId")
		case ok && tenantAnn:
			sub.Notes = append(sub.Notes, "tenant from the ServiceAccount's annotation")
		case ok:
			sub.Refusals = append(sub.Refusals, "ESO refuses this store: no tenant (neither the store's tenantId nor the ServiceAccount's annotation)")
		}
		return sub
	}
	sub.Mode += " (legacy SDK)"
	// the legacy guards, in ESO's order (keyvault.go 1111-1195)
	authRef := mapField(p, "authSecretRef")
	switch {
	case authRef != nil && authRef["clientId"] == nil:
		sub.Refusals = append(sub.Refusals, "ESO refuses this store: authSecretRef has no clientId, checked before the ServiceAccount's annotation")
		return sub
	case authRef != nil:
		sub.Unresolved = append(sub.Unresolved, "the client ID comes from a Secret (not read)")
		if clientAnn {
			sub.Unresolved = append(sub.Unresolved, "with the client annotation also present, ESO refuses only if that Secret's value is non-empty")
		}
	case ok && !clientAnn:
		sub.Refusals = append(sub.Refusals, "ESO refuses this store: no client ID (no authSecretRef.clientId and no "+AnnAzureClient+")")
	}
	switch {
	case authRef != nil && authRef["tenantId"] != nil:
		sub.Unresolved = append(sub.Unresolved, "the tenant comes from a Secret (not read)")
	case storeTenantSet && tenantStore != "" && tenantAnn:
		sub.Refusals = append(sub.Refusals, "ESO refuses this store: the store's tenantId and the ServiceAccount's tenant annotation are both set")
	case storeTenantSet && tenantStore != "":
		sub.Notes = append(sub.Notes, "tenant from the store's tenantId")
	case tenantAnn:
		sub.Notes = append(sub.Notes, "tenant from the ServiceAccount's annotation")
	case ok:
		sub.Unresolved = append(sub.Unresolved, "the tenant falls back to the controller's AZURE_TENANT_ID, which is not visible")
	}
	return sub
}

func gcpSubject(p map[string]any, kind, ns string, get SALookup) Subject {
	sub := Subject{Provider: "gcpsm"}
	auth := mapField(p, "auth")
	gsaOf := func(sa *corev1.ServiceAccount) string {
		if g, has := sa.Annotations[AnnGKEServiceAcct]; has {
			return valueOf(g, shapeEmail).Shown
		}
		return ""
	}
	switch {
	case mapField(auth, "secretRef") != nil:
		sub.Mode, sub.Base = "secretRef", "static credentials from a Secret (never read)"
	case mapField(auth, "workloadIdentity") != nil:
		sub.Mode = "workloadIdentity"
		sub.SA = saRef(mapField(auth, "workloadIdentity", "serviceAccountRef"), kind, ns, &sub)
		sub.Base = "the ServiceAccount's token, minted by ESO itself"
		if sa, ok := lookupSA(sub.SA, get, &sub); ok {
			if g := gsaOf(sa); g != "" {
				sub.Requested = append(sub.Requested, g)
			} else {
				sub.Notes = append(sub.Notes, "no linked Google service account: the federated principal itself")
			}
		}
	case mapField(auth, "workloadIdentityFederation") != nil:
		f := mapField(auth, "workloadIdentityFederation")
		sub.Mode = "workloadIdentityFederation"
		email, hasEmail := strField(f, "gcpServiceAccountEmail")
		switch {
		case mapField(f, "serviceAccountRef") != nil:
			sub.SA = saRef(mapField(f, "serviceAccountRef"), kind, ns, &sub)
			sub.Base = "the ServiceAccount's token, minted by ESO itself"
			sa, ok := lookupSA(sub.SA, get, &sub)
			switch {
			case hasEmail && email != "":
				sub.Notes = append(sub.Notes, "gcpServiceAccountEmail overrides the ServiceAccount's linked account")
			case ok && gsaOf(sa) != "":
				sub.Requested = append(sub.Requested, gsaOf(sa))
			}
		case mapField(f, "credConfig") != nil:
			sub.Base = "a credential configuration from a ConfigMap (not read)"
			sub.Unresolved = append(sub.Unresolved, "the credential configuration is not read")
		default:
			sub.Base = "AWS security credentials (not read)"
			sub.Unresolved = append(sub.Unresolved, "the AWS credentials are not read")
		}
		// ESO applies the override after merging every base: a configured
		// impersonation target is known without reading the base's source
		if hasEmail && email != "" {
			sub.Requested = append(sub.Requested, valueOf(email, shapeEmail).Shown)
		}
	default:
		sub.Mode, sub.Base, sub.Controller = "default", "the handling controller's application default credentials", true
	}
	return sub
}

// ControllerPod is one running pod of a candidate ESO controller.
type ControllerPod struct {
	Pod   Pod
	Class string // its own --controller-class ("default" when absent)
	// ClassUnresolved: the argument expands at runtime, or the class comes
	// from somewhere Klyx does not read.
	ClassUnresolved bool
	// Owned: its ReplicaSet owner belongs to the candidate (by UID).
	Owned bool
	// Phase: the pod's phase. Only a Running pod's process handles stores;
	// a Succeeded or Failed pod has departed, and a Pending or Unknown one
	// proves nothing yet.
	Phase string
}

// Candidate is one candidate controller Deployment and its running pods.
type Candidate struct {
	Namespace, Name, UID string
	Pods                 []ControllerPod
	// PodsComplete: the pod list was read completely (not capped,
	// continued or forbidden).
	PodsComplete bool
}

// Group is the applicable pods running as one ServiceAccount.
type Group struct {
	Namespace      string `json:"namespace"`
	ServiceAccount string `json:"serviceAccount"`
	Candidate      string `json:"candidate"`
	Pods           []Pod  `json:"pods"`
}

// Handlers is who handles a store (S5 §4.2): the applicable running pods
// grouped by their own ServiceAccount, or why that is not resolved.
type Handlers struct {
	Groups     []Group `json:"groups,omitempty"`
	Unresolved string  `json:"unresolved,omitempty"`
	// NotRunning names applicable owned pods that are not running yet
	// (Pending or Unknown): not counted as handlers, never as absent.
	NotRunning []string `json:"notRunning,omitempty"`
}

// ClassOf reads a controller container's --controller-class argument as
// ESO's flag parsing does: the last assignment wins, and parsing stops at
// "--".
func ClassOf(c corev1.Container) (string, bool) {
	args := append(append([]string(nil), c.Command...), c.Args...)
	class, set := "default", false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			i = len(args)
		case strings.HasPrefix(a, "--controller-class="):
			class, set = strings.TrimPrefix(a, "--controller-class="), true
		case a == "--controller-class" && i+1 < len(args):
			class, set = args[i+1], true
			i++
		}
	}
	if set && strings.Contains(class, "$(") {
		return "", false
	}
	return class, true
}

// HandlersOf applies ESO's ShouldProcessStore per running pod: an empty
// store class accepts every pod; otherwise the pod's own class must equal
// it. The filter comes before grouping; an incomplete read, or an
// unresolved class on a pod that could apply, names no handler.
func HandlersOf(storeClass string, cands []Candidate, candidatesComplete bool) Handlers {
	if !candidatesComplete {
		return Handlers{Unresolved: "the candidate controllers were not read completely"}
	}
	groups := map[string]*Group{}
	var notRunning []string
	for _, c := range cands {
		if !c.PodsComplete {
			return Handlers{Unresolved: "the pods of " + c.Namespace + "/" + c.Name + " were not read completely"}
		}
		for _, p := range c.Pods {
			if !p.Owned {
				continue // not proven to be this candidate's
			}
			if p.Phase == string(corev1.PodSucceeded) || p.Phase == string(corev1.PodFailed) {
				continue // departed: its process handles nothing
			}
			if storeClass != "" {
				if p.ClassUnresolved {
					return Handlers{Unresolved: "pod " + p.Pod.Name + "'s controller class is resolved only at runtime"}
				}
				if p.Class != storeClass {
					continue
				}
			}
			if p.Phase != string(corev1.PodRunning) {
				notRunning = append(notRunning, p.Pod.Name+" ("+phaseOr(p.Phase)+")")
				continue
			}
			key := p.Pod.Namespace + "/" + p.Pod.ServiceAccount + "/" + c.Namespace + "/" + c.Name
			g := groups[key]
			if g == nil {
				g = &Group{Namespace: p.Pod.Namespace, ServiceAccount: p.Pod.ServiceAccount, Candidate: c.Namespace + "/" + c.Name}
				groups[key] = g
			}
			g.Pods = append(g.Pods, p.Pod)
		}
	}
	sort.Strings(notRunning)
	if len(groups) == 0 {
		if len(notRunning) > 0 {
			return Handlers{Unresolved: "the controller pods that accept this store are not running yet", NotRunning: notRunning}
		}
		return Handlers{Unresolved: "no running controller pod Klyx found accepts this store"}
	}
	out := Handlers{NotRunning: notRunning}
	for _, g := range groups {
		out.Groups = append(out.Groups, *g)
	}
	sort.Slice(out.Groups, func(i, j int) bool {
		a, b := out.Groups[i], out.Groups[j]
		return a.Candidate+a.Namespace+a.ServiceAccount < b.Candidate+b.Namespace+b.ServiceAccount
	})
	return out
}

func phaseOr(p string) string {
	if p == "" {
		return "phase unknown"
	}
	return p
}

// ControllerEnvGaps names, for Azure's controller-env branch, the variables
// absent from a group's pods' env (S5 §4.2): said as not in the pod spec,
// never as ESO failing.
func ControllerEnvGaps(g Group) []string {
	var gaps []string
	for _, name := range []string{EnvAzureClient, EnvAzureTenant, EnvAzureToken} {
		for _, p := range g.Pods {
			found := false
			for _, c := range p.Containers {
				if _, ok := c.Env[name]; ok {
					found = true
				}
			}
			if !found {
				gaps = append(gaps, name+" is not in pod "+p.Name+"'s spec; envFrom sources and the image's environment are not read")
			}
		}
	}
	return gaps
}
