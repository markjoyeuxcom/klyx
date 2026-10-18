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
	// Audience of the resolved token entry, when the chain reached one: its
	// exact bytes compare, and only a policy-checked form is shown.
	Audience *Value `json:"audience,omitempty"`
	// The identifiers compared; a Value serialises only what may be shown.
	Role   *Value `json:"role,omitempty"`
	Client *Value `json:"client,omitempty"`
	Tenant *Value `json:"tenant,omitempty"`
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
	Namespace      string    `json:"namespace"`
	Name           string    `json:"name"`
	UID            string    `json:"uid"`
	ServiceAccount string    `json:"serviceAccount"`
	Node           string    `json:"node,omitempty"`
	Created        time.Time `json:"created"`
	HostNetwork    bool      `json:"hostNetwork,omitempty"`
	// AWSOptOut: the self-hosted webhook's opt-out label.
	AWSOptOut bool `json:"awsOptOut,omitempty"`
	// PodIdentityLabel: the v0.6.15+ webhook's result label.
	PodIdentityLabel bool `json:"podIdentityLabel,omitempty"`
	// AzureOptIn: the exact pod label azure.workload.identity/use: "true".
	AzureOptIn bool        `json:"azureOptIn"`
	Containers []Container `json:"containers"`
	// ContainersCut and VolumesCut: past the bounds, said.
	ContainersCut int    `json:"containersCut,omitempty"`
	VolumesCut    bool   `json:"volumesCut,omitempty"`
	OwnerUID      string `json:"-"`
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
	vols := volumes{tokens: map[string][]tokenEntry{}, projected: map[string]bool{}, cut: map[string]bool{}}
	sources := 0
	for _, v := range p.Spec.Volumes {
		if v.Projected == nil {
			continue
		}
		vols.projected[v.Name] = true
		for _, src := range v.Projected.Sources {
			if src.ServiceAccountToken == nil {
				continue
			}
			if sources == MaxVolumeSources {
				// this volume's later entries, and every later volume's,
				// are not read: a token path in them is unresolved
				out.VolumesCut, vols.cut[v.Name] = true, true
				break
			}
			sources++
			vols.tokens[v.Name] = append(vols.tokens[v.Name], tokenEntry{path: src.ServiceAccountToken.Path, audience: src.ServiceAccountToken.Audience})
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
		out.Containers = append(out.Containers, containerOf(e.c, e.init, vols, awsSkip, azSkip))
	}
	return out
}

func containerOf(c corev1.Container, init bool, vols volumes, awsSkip, azSkip map[string]bool) Container {
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
	ch := chainer{mounts: c.VolumeMounts, vols: vols}
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
		m.State, m.Missing, m.Audience = ChainPartial, "the token's audience is "+audienceShown(aud)+", not "+want, audienceValue(aud)
	default:
		m.State, m.Audience = ChainComplete, audienceValue(aud)
	}
	return m, true
}

// audienceValue is a token entry's audience: compared by its exact bytes,
// shown only in a token's shape (S5 §7); empty is the API server's own.
func audienceValue(a string) *Value {
	if a == "" {
		return &Value{Shown: "the API server's own"}
	}
	v := valueOf(a, shapeToken)
	return &v
}

func audienceShown(a string) string { return audienceValue(a).Shown }

const (
	errUnresolvedMount = "a mount uses subPathExpr, resolved only at runtime"
	errVolumesCut      = "the pod's projected volumes were cut at the bound, so the token was not read"
)

// volumes is what a pod's projected volumes hold, as far as they were read.
type volumes struct {
	tokens    map[string][]tokenEntry // volume name -> retained token entries
	projected map[string]bool         // every projected volume, read or not
	cut       map[string]bool         // a volume whose entries the bound cut
}

// chainer resolves a token file through a container's mounts to a
// projected serviceAccountToken entry (S5 §3.2).
type chainer struct {
	mounts []corev1.VolumeMount
	vols   volumes
}

// splitDrive turns a Windows path (D:\var\run\...) into its lower-case
// drive and the slash form the mounts use; a path without a drive has "".
func splitDrive(p string) (string, string) {
	drive := ""
	if len(p) >= 2 && p[1] == ':' && ((p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z')) {
		drive, p = strings.ToLower(p[:1]), p[2:]
	}
	return drive, path.Clean("/" + strings.ReplaceAll(p, "\\", "/"))
}

// sameDrive: a path without a drive is on C:, Windows' default; two drives
// must be the same drive.
func sameDrive(a, b string) bool {
	if a == "" {
		a = "c"
	}
	if b == "" {
		b = "c"
	}
	return a == b
}

// resolve returns the token's audience, or the missing link.
func (c chainer) resolve(file string) (string, string) {
	fd, f := splitDrive(file)
	var best *corev1.VolumeMount
	bestPath := ""
	for i := range c.mounts {
		md, mp := splitDrive(c.mounts[i].MountPath)
		if !sameDrive(fd, md) {
			continue
		}
		if f == mp || strings.HasPrefix(f, strings.TrimSuffix(mp, "/")+"/") {
			if best == nil || len(mp) > len(bestPath) {
				best, bestPath = &c.mounts[i], mp
			}
		}
	}
	if best == nil {
		return "", "no mount in this container holds the token file"
	}
	if best.SubPathExpr != "" {
		return "", errUnresolvedMount
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(f, bestPath), "/")
	if best.SubPath != "" {
		rel = path.Join(best.SubPath, rel)
	}
	for _, e := range c.vols.tokens[best.Name] {
		if path.Clean(e.path) == path.Clean(rel) {
			return e.audience, ""
		}
	}
	switch {
	case !c.vols.projected[best.Name]:
		return "", "the mounted volume " + best.Name + " is not a projected token volume"
	case c.vols.cut[best.Name]:
		// its entries past the bound were not read: absence proves nothing
		return "", errVolumesCut
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
