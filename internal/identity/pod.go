package identity

import (
	"path"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// Bounds (S5 §6).
const (
	MaxContainers    = 16 // init and regular containers read per pod
	MaxVolumeSources = 16 // projected volume sources read per pod
)

// Mechanisms.
const (
	MechIRSA        = "irsa"
	MechPodIdentity = "pod-identity"
	MechAzure       = "azure"
)

// Chain states for one mechanism in one container (S5 §3.2).
const (
	ChainComplete   = "complete"
	ChainPartial    = "partial"
	ChainUnresolved = "unresolved"
	ChainNone       = "none"
)

// EnvValue is one named identity variable in a container's env.
type EnvValue struct {
	Value Value `json:"value"`
	// FromRef: set by valueFrom (a Secret, ConfigMap or field; never read).
	FromRef bool `json:"fromRef,omitempty"`
	// Expansion: holds $(VAR), resolved only at runtime.
	Expansion bool `json:"expansion,omitempty"`
	Oversized bool `json:"oversized,omitempty"`
}

func (e EnvValue) resolved() bool { return !e.FromRef && !e.Expansion && !e.Oversized }

// Mechanism is one mechanism's reading in one container.
type Mechanism struct {
	Kind  string `json:"kind"`
	State string `json:"state"` // complete | partial | unresolved
	// Missing names the absent link for a partial chain, or the unresolved
	// value.
	Missing string `json:"missing,omitempty"`
	// Audience of the resolved token entry, when the chain reached one.
	Audience string `json:"audience,omitempty"`
	// The identifiers compared (exact values kept unexported in Value).
	Role, Client, Tenant *Value `json:"-"`
	// URI is Pod Identity's credentials URI, as shown.
	URI *Value `json:"uri,omitempty"`
}

// Container is one container's identity evidence.
type Container struct {
	Name string `json:"name"`
	Init bool   `json:"init,omitempty"`
	// Env holds the named identity variables only (S5 §7).
	Env map[string]EnvValue `json:"env,omitempty"`
	// EnvFrom: the container has envFrom sources (never read).
	EnvFrom    bool        `json:"envFrom,omitempty"`
	Mechanisms []Mechanism `json:"mechanisms,omitempty"`
	// Skipped: named in an adapter's skip-containers annotation.
	SkippedAWS   bool `json:"skippedAws,omitempty"`
	SkippedAzure bool `json:"skippedAzure,omitempty"`
}

// Pod is one pod's identity evidence.
type Pod struct {
	Namespace, Name, UID string
	ServiceAccount       string    `json:"serviceAccount"`
	Node                 string    `json:"node,omitempty"`
	Created              time.Time `json:"created"`
	HostNetwork          bool      `json:"hostNetwork,omitempty"`
	// AWSOptOut: the self-hosted webhook's opt-out label.
	AWSOptOut bool `json:"awsOptOut,omitempty"`
	// PodIdentityLabel: the v0.6.15+ webhook's result label.
	PodIdentityLabel bool `json:"podIdentityLabel,omitempty"`
	// AzureOptIn: the exact pod label azure.workload.identity/use: "true".
	AzureOptIn bool        `json:"azureOptIn"`
	Containers []Container `json:"containers"`
	// ContainersCut and VolumesCut: past the bounds, said.
	ContainersCut int  `json:"containersCut,omitempty"`
	VolumesCut    bool `json:"volumesCut,omitempty"`
	OwnerUID      string
}

// tokenEntry is one projected serviceAccountToken source.
type tokenEntry struct {
	path, audience string
}

// skipSet splits a skip-containers annotation by sep.
func skipSet(s, sep string) map[string]bool {
	out := map[string]bool{}
	for _, part := range strings.Split(s, sep) {
		if p := strings.TrimSpace(part); p != "" {
			out[p] = true
		}
	}
	return out
}

// PodOf reads a pod's identity evidence. Only the named identity variables
// are kept; every other env entry is dropped here, unread (S5 §7).
func PodOf(p *corev1.Pod) Pod {
	out := Pod{Namespace: p.Namespace, Name: p.Name, UID: string(p.UID), ServiceAccount: p.Spec.ServiceAccountName, Node: p.Spec.NodeName,
		Created: p.CreationTimestamp.Time, HostNetwork: p.Spec.HostNetwork}
	if out.ServiceAccount == "" {
		out.ServiceAccount = "default"
	}
	_, out.AWSOptOut = p.Labels[LabelAWSOptOut]
	out.PodIdentityLabel = p.Labels[LabelPodIdentity] == "enabled"
	out.AzureOptIn = p.Labels[LabelAzureUse] == "true"
	for _, o := range p.OwnerReferences {
		if o.Controller != nil && *o.Controller {
			out.OwnerUID = string(o.UID)
		}
	}
	tokens := map[string][]tokenEntry{} // projected volume name -> token entries
	sources := 0
	for _, v := range p.Spec.Volumes {
		if v.Projected == nil {
			continue
		}
		for _, src := range v.Projected.Sources {
			if src.ServiceAccountToken == nil {
				continue
			}
			if sources == MaxVolumeSources {
				out.VolumesCut = true
				break
			}
			sources++
			tokens[v.Name] = append(tokens[v.Name], tokenEntry{path: src.ServiceAccountToken.Path, audience: src.ServiceAccountToken.Audience})
		}
	}
	awsSkip := skipSet(p.Annotations[AnnAWSSkip], ",")
	azSkip := skipSet(p.Annotations[AnnAzureSkip], ";")
	all := make([]struct {
		c    corev1.Container
		init bool
	}, 0, len(p.Spec.InitContainers)+len(p.Spec.Containers))
	for _, c := range p.Spec.InitContainers {
		all = append(all, struct {
			c    corev1.Container
			init bool
		}{c, true})
	}
	for _, c := range p.Spec.Containers {
		all = append(all, struct {
			c    corev1.Container
			init bool
		}{c, false})
	}
	for i, e := range all {
		if i == MaxContainers {
			out.ContainersCut = len(all) - MaxContainers
			break
		}
		out.Containers = append(out.Containers, containerOf(e.c, e.init, tokens, awsSkip, azSkip, out.VolumesCut))
	}
	return out
}

func containerOf(c corev1.Container, init bool, tokens map[string][]tokenEntry, awsSkip, azSkip map[string]bool, volumesCut bool) Container {
	out := Container{Name: c.Name, Init: init, EnvFrom: len(c.EnvFrom) > 0, SkippedAWS: awsSkip[c.Name], SkippedAzure: azSkip[c.Name]}
	for _, e := range c.Env {
		if !identityEnv[e.Name] {
			continue // never read
		}
		if out.Env == nil {
			out.Env = map[string]EnvValue{}
		}
		ev := EnvValue{}
		switch {
		case e.ValueFrom != nil:
			ev.FromRef = true
			ev.Value = Value{Shown: "set from a reference (not read)"}
		case len(e.Value) > MaxValue:
			ev.Oversized = true
			ev.Value = Value{raw: e.Value, Shown: "oversized value (not shown)", Unexpected: true}
		case strings.Contains(e.Value, "$("):
			ev.Expansion = true
			ev.Value = Value{raw: e.Value, Shown: "expands at runtime (not shown)", Unexpected: true}
		default:
			ev.Value = valueOf(e.Value, envShape[e.Name])
		}
		out.Env[e.Name] = ev
	}
	ch := chainer{mounts: c.VolumeMounts, tokens: tokens, volumesCut: volumesCut}
	if m, ok := mechanismOf(MechIRSA, out.Env, []string{EnvRoleARN, EnvWebTokenFile}, EnvWebTokenFile, "", ch); ok {
		m.Role = valPtr(out.Env, EnvRoleARN)
		out.Mechanisms = append(out.Mechanisms, m)
	}
	if m, ok := mechanismOf(MechPodIdentity, out.Env, []string{EnvCredsURI, EnvCredsToken}, EnvCredsToken, AudiencePodIdentity, ch); ok {
		m.URI = valPtr(out.Env, EnvCredsURI)
		out.Mechanisms = append(out.Mechanisms, m)
	}
	if m, ok := mechanismOf(MechAzure, out.Env, []string{EnvAzureClient, EnvAzureTenant, EnvAzureToken}, EnvAzureToken, AudienceAzure, ch); ok {
		m.Client, m.Tenant = valPtr(out.Env, EnvAzureClient), valPtr(out.Env, EnvAzureTenant)
		out.Mechanisms = append(out.Mechanisms, m)
	}
	return out
}

func valPtr(env map[string]EnvValue, name string) *Value {
	if e, ok := env[name]; ok && e.resolved() {
		v := e.Value
		return &v
	}
	return nil
}

// mechanismOf reads one mechanism: none when no variable of it is present;
// unresolved when any present one is not a literal; partial when any is
// missing or the token file's chain is broken; else complete. want is the
// required audience ("" for IRSA, compared with the declaration later).
func mechanismOf(kind string, env map[string]EnvValue, vars []string, fileVar, want string, ch chainer) (Mechanism, bool) {
	m := Mechanism{Kind: kind}
	present := 0
	var missing []string
	for _, v := range vars {
		e, ok := env[v]
		if !ok {
			missing = append(missing, v)
			continue
		}
		present++
		if !e.resolved() {
			m.State, m.Missing = ChainUnresolved, v+" is not a literal value"
		}
	}
	if present == 0 {
		return m, false
	}
	if m.State == ChainUnresolved {
		return m, true
	}
	if len(missing) > 0 {
		m.State, m.Missing = ChainPartial, strings.Join(missing, ", ")+" not in the pod spec's env"
		return m, true
	}
	aud, gap := ch.resolve(env[fileVar].Value.raw)
	switch {
	case gap == errUnresolvedMount || gap == errVolumesCut:
		m.State, m.Missing = ChainUnresolved, gap
	case gap != "":
		m.State, m.Missing = ChainPartial, gap
	case want != "" && aud != want:
		m.State, m.Missing, m.Audience = ChainPartial, "the token's audience is "+audienceShown(aud)+", not "+want, aud
	default:
		m.State, m.Audience = ChainComplete, aud
	}
	return m, true
}

func audienceShown(a string) string {
	if a == "" {
		return "the API server's own"
	}
	v := valueOf(a, shapeToken)
	return v.Shown
}

const (
	errUnresolvedMount = "a mount uses subPathExpr, resolved only at runtime"
	errVolumesCut      = "the pod's projected volumes were cut at the bound, so the token was not read"
)

// chainer resolves a token file through a container's mounts to a
// projected serviceAccountToken entry (S5 §3.2).
type chainer struct {
	mounts     []corev1.VolumeMount
	tokens     map[string][]tokenEntry
	volumesCut bool
}

// normalise turns a Windows path (C:\var\run\...) into the slash form the
// mounts use, for matching.
func normalise(p string) string {
	if len(p) > 2 && p[1] == ':' {
		p = p[2:]
	}
	return strings.ReplaceAll(p, "\\", "/")
}

// resolve returns the token's audience, or the missing link.
func (c chainer) resolve(file string) (string, string) {
	f := path.Clean(normalise(file))
	var best *corev1.VolumeMount
	for i := range c.mounts {
		mp := path.Clean(normalise(c.mounts[i].MountPath))
		if f == mp || strings.HasPrefix(f, strings.TrimSuffix(mp, "/")+"/") {
			if best == nil || len(mp) > len(path.Clean(normalise(best.MountPath))) {
				best = &c.mounts[i]
			}
		}
	}
	if best == nil {
		return "", "no mount in this container holds the token file"
	}
	if best.SubPathExpr != "" {
		return "", errUnresolvedMount
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(f, path.Clean(normalise(best.MountPath))), "/")
	if best.SubPath != "" {
		rel = path.Join(best.SubPath, rel)
	}
	entries, ok := c.tokens[best.Name]
	if !ok {
		if c.volumesCut {
			return "", errVolumesCut
		}
		return "", "the mounted volume " + best.Name + " is not a projected token volume"
	}
	for _, e := range entries {
		if path.Clean(e.path) == path.Clean(rel) {
			return e.audience, ""
		}
	}
	return "", "the volume " + best.Name + " has no token at that path"
}

// SortPods orders a page by age, newest first (S5 §6: within the page read).
func SortPods(ps []Pod) {
	sort.SliceStable(ps, func(i, j int) bool {
		if !ps[i].Created.Equal(ps[j].Created) {
			return ps[i].Created.After(ps[j].Created)
		}
		return ps[i].Name < ps[j].Name
	})
}
