package appbridge

import (
	"context"
	"time"

	"github.com/moomora/klyx/internal/crd"
	"github.com/moomora/klyx/internal/fleet"
)

// CRDConn is the per-cluster read surface CRDService needs.
type CRDConn interface {
	ListCRDs(ctx context.Context) ([]crd.Info, error)
	CountResource(ctx context.Context, group, version, plural string) (int, bool, error)
	ListInstances(ctx context.Context, group, version, plural string, limit int64, continueToken string) ([]crd.InstanceMeta, string, error)
	GetInstanceDetail(ctx context.Context, group, version, plural, ns, name string) (crd.InstanceDetail, error)
	ApplyResourceYAML(ctx context.Context, group, version, plural, kind, scope, ns, name, yaml string, dryRun bool) error
	CreateResourceYAML(ctx context.Context, yamlDocs string, dryRun bool) []fleet.AdhocResult
	ListAdhocObjects(ctx context.Context) []fleet.AdhocObject
	DeleteAdhocObject(ctx context.Context, group, version, plural, ns, name string) error
	RevealSecretKey(ctx context.Context, ns, name, key string) (string, error)
	GitOpsOwnership(ctx context.Context, group, kind, ns, name string) fleet.GitOpsOwnership
}

const crdTimeout = 30 * time.Second

const instancePageSize = 100

// CRDService is bound to JS. Pure request/response: ListCRDs returns the grouped
// tree (no counts); CountKind lazily counts one kind on group-expand.
type CRDService struct {
	lookup func(string) (CRDConn, bool)
}

func NewCRDService(lookup func(string) (CRDConn, bool)) *CRDService {
	return &CRDService{lookup: lookup}
}

// ListCRDs returns the cluster's CRDs grouped by API group with category and
// sorted deterministically. Empty on a cluster miss or a list error.
func (s *CRDService) ListCRDs(cluster string) []CRDGroupDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return []CRDGroupDTO{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), crdTimeout)
	defer cancel()
	infos, err := conn.ListCRDs(ctx)
	if err != nil {
		return []CRDGroupDTO{}
	}
	return groupCRDs(infos)
}

// CountKind returns the hybrid instance count for one kind. Zero value on miss.
func (s *CRDService) CountKind(cluster, group, version, plural string) CRDCountDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return CRDCountDTO{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), crdTimeout)
	defer cancel()
	count, capped, err := conn.CountResource(ctx, group, version, plural)
	if err != nil {
		return CRDCountDTO{}
	}
	return CRDCountDTO{Count: count, Capped: capped}
}

func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// GetInstanceDetail returns the full per-instance detail. Zero value on miss/error.
func (s *CRDService) GetInstanceDetail(cluster, group, version, plural, namespace, name string) InstanceDetailDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return InstanceDetailDTO{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), crdTimeout)
	defer cancel()
	d, err := conn.GetInstanceDetail(ctx, group, version, plural, namespace, name)
	if err != nil {
		return InstanceDetailDTO{}
	}
	labels := d.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	annotations := d.Annotations
	if annotations == nil {
		annotations = map[string]string{}
	}
	conds := make([]ConditionDTO, 0, len(d.Conditions))
	for _, c := range d.Conditions {
		conds = append(conds, ConditionDTO{Type: c.Type, Status: c.Status, Reason: c.Reason, Message: c.Message, Scope: c.Scope})
	}
	facts := make([]FactDTO, 0, len(d.Facts))
	for _, f := range d.Facts {
		facts = append(facts, FactDTO{Group: f.Group, Label: f.Label, Value: f.Value, Tone: f.Tone, Kind: f.Kind, Pct: f.Pct})
	}
	events := make([]EventDTO, 0, len(d.Events))
	for _, e := range d.Events {
		events = append(events, EventDTO{Type: e.Type, Reason: e.Reason, Message: e.Message, Count: int(e.Count), LastSeen: rfc3339(e.Last)})
	}
	secretKeys := make([]SecretKeyDTO, 0, len(d.SecretKeys))
	for _, sk := range d.SecretKeys {
		secretKeys = append(secretKeys, SecretKeyDTO{Key: sk.Key, Bytes: sk.Bytes})
	}
	var serviceBacking *ServiceBackingDTO
	if d.ServiceBacking != nil {
		b := d.ServiceBacking
		ports := make([]ServicePortDTO, 0, len(b.Ports))
		for _, p := range b.Ports {
			ports = append(ports, ServicePortDTO{Name: p.Name, Port: p.Port, Protocol: p.Protocol})
		}
		addrs := make([]EndpointAddrDTO, 0, len(b.Addresses))
		for _, a := range b.Addresses {
			addrs = append(addrs, EndpointAddrDTO{IP: a.IP, Ready: a.Ready, TargetKind: a.TargetKind, TargetName: a.TargetName})
		}
		sel := b.Selector
		if sel == nil {
			sel = map[string]string{}
		}
		serviceBacking = &ServiceBackingDTO{
			Ports:     ports,
			Ready:     b.Ready,
			NotReady:  b.NotReady,
			Addresses: addrs,
			Selector:  sel,
		}
	}
	var hpaScaling *HPAScalingDTO
	if d.HPAScaling != nil {
		h := d.HPAScaling
		mets := make([]HPAMetricDTO, 0, len(h.Metrics))
		for _, m := range h.Metrics {
			mets = append(mets, HPAMetricDTO{Name: m.Name, Type: m.Type, Target: m.Target, Current: m.Current})
		}
		hpaScaling = &HPAScalingDTO{
			MinReplicas:     h.MinReplicas,
			MaxReplicas:     h.MaxReplicas,
			CurrentReplicas: h.CurrentReplicas,
			DesiredReplicas: h.DesiredReplicas,
			TargetKind:      h.TargetKind,
			TargetName:      h.TargetName,
			LastScaleUnix:   h.LastScaleUnix,
			Metrics:         mets,
		}
	}
	related := make([]RelatedRefDTO, 0, len(d.Related))
	for _, r := range d.Related {
		related = append(related, RelatedRefDTO{
			Kind: r.Kind, Namespace: r.Namespace, Name: r.Name,
			Group: r.Group, Version: r.Version, Plural: r.Plural,
			Scope: r.Scope, Relation: r.Relation,
		})
	}
	return InstanceDetailDTO{
		Kind: d.Kind, Namespace: d.Namespace, Name: d.Name,
		Created: rfc3339(d.Created), Labels: labels, Annotations: annotations,
		Conditions: conds, Events: events, YAML: d.YAML,
		SecretKeys:     secretKeys,
		ServiceBacking: serviceBacking,
		HPAScaling:     hpaScaling,
		Related:        related,
		Facts:          facts,
	}
}

// ApplyResourceYAML validates and server-side-applies the YAML for the resource
// currently open in the generic resource detail. dryRun=true asks the apiserver
// to validate/admit without persisting.
func (s *CRDService) ApplyResourceYAML(cluster, group, version, plural, kind, scope, namespace, name, yaml string, dryRun bool) ActionResultDTO {
	// Dry-run mutates nothing; only a real apply consumes the protection grant.
	if !dryRun {
		if err := checkProtected(cluster); err != nil {
			return ActionResultDTO{Error: err.Error()}
		}
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		return ActionResultDTO{Error: "cluster not connected: " + cluster}
	}
	ctx, cancel := context.WithTimeout(context.Background(), crdTimeout)
	defer cancel()
	if err := conn.ApplyResourceYAML(ctx, group, version, plural, kind, scope, namespace, name, yaml, dryRun); err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	return ActionResultDTO{OK: true}
}

// CreateResourceYAML creates the documents in yaml as NEW ad-hoc objects (the
// deliberately-non-GitOps scratch path: stamped klyx.dev/adhoc, plain create -
// name collisions refuse, Secrets refused, explicit namespaces required). A
// real create on a protected cluster consumes the one-shot typed-confirmation
// grant, exactly like ApplyResourceYAML; dry-run never does.
func (s *CRDService) CreateResourceYAML(cluster, yaml string, dryRun bool) []AdhocResultDTO {
	if !dryRun {
		if err := checkProtected(cluster); err != nil {
			return []AdhocResultDTO{{Error: err.Error()}}
		}
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		return []AdhocResultDTO{{Error: "cluster not connected: " + cluster}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), crdTimeout)
	defer cancel()
	rs := conn.CreateResourceYAML(ctx, yaml, dryRun)
	out := make([]AdhocResultDTO, 0, len(rs))
	for _, r := range rs {
		out = append(out, AdhocResultDTO{
			Kind: r.Kind, Namespace: r.Namespace, Name: r.Name, OK: r.OK, Error: r.Error,
			NamespaceOwnerKind: r.NamespaceOwnerKind, NamespaceOwnerName: r.NamespaceOwnerName,
			NamespaceOwnerNamespace: r.NamespaceOwnerNamespace, NamespacePrunes: r.NamespacePrunes,
		})
	}
	return out
}

// ListAdhocObjects returns everything Klyx created ad-hoc on the cluster (the
// scratch inventory: label klyx.dev/adhoc). Read-only.
func (s *CRDService) ListAdhocObjects(cluster string) []AdhocObjectDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return []AdhocObjectDTO{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), crdTimeout)
	defer cancel()
	objs := conn.ListAdhocObjects(ctx)
	out := make([]AdhocObjectDTO, 0, len(objs))
	for _, o := range objs {
		out = append(out, AdhocObjectDTO{
			Kind: o.Kind, Group: o.Group, Version: o.Version, Plural: o.Plural,
			Scope: o.Scope, Namespace: o.Namespace, Name: o.Name, AgeSeconds: o.AgeSeconds,
		})
	}
	return out
}

// DeleteAdhocObject deletes one Klyx-created scratch object. Label-guarded in
// the data layer (never deletes anything Klyx did not create) and consumes the
// protected-cluster grant like every destructive verb.
func (s *CRDService) DeleteAdhocObject(cluster, group, version, plural, namespace, name string) ActionResultDTO {
	if err := checkProtected(cluster); err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		return ActionResultDTO{Error: "cluster not connected: " + cluster}
	}
	ctx, cancel := context.WithTimeout(context.Background(), crdTimeout)
	defer cancel()
	if err := conn.DeleteAdhocObject(ctx, group, version, plural, namespace, name); err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	return ActionResultDTO{OK: true}
}

// GitOpsOwnership returns the authoritative pre-write verdict for a target
// object: the GitOps unit that OWNS it (edits revert) and the unit that REACHES
// its namespace (out-of-band creates are pruned). Read-only; called by the edit
// flow before a write and by the (future) create composer. A cluster miss
// returns an empty verdict with a warning - degraded checks are never silent.
func (s *CRDService) GitOpsOwnership(cluster, group, kind, namespace, name string) GitOpsOwnershipDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return GitOpsOwnershipDTO{Warnings: []string{"cluster not connected: " + cluster}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), crdTimeout)
	defer cancel()
	o := conn.GitOpsOwnership(ctx, group, kind, namespace, name)
	dto := GitOpsOwnershipDTO{
		OwnerKind: o.OwnerKind, OwnerNamespace: o.OwnerNamespace, OwnerName: o.OwnerName,
		OwnerPrunes: o.OwnerPrunes, OwnerSuspended: o.OwnerSuspended, Basis: o.Basis,
		NamespaceOwnerKind: o.NamespaceOwnerKind, NamespaceOwnerNamespace: o.NamespaceOwnerNamespace,
		NamespaceOwnerName: o.NamespaceOwnerName, NamespacePrunes: o.NamespacePrunes,
		NamespaceBasis: o.NamespaceBasis,
		Warnings:       []string{},
	}
	if o.Warnings != nil {
		dto.Warnings = o.Warnings
	}
	return dto
}

// RevealSecretKey fetches the decoded value of one key from a Secret and
// returns it. The value is never logged. On any error (cluster miss, secret not
// found, key absent) the Error field is set and Value is "".
func (s *CRDService) RevealSecretKey(cluster, namespace, name, key string) RevealResultDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return RevealResultDTO{Error: "cluster not found"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), crdTimeout)
	defer cancel()
	val, err := conn.RevealSecretKey(ctx, namespace, name, key)
	if err != nil {
		return RevealResultDTO{Error: err.Error()}
	}
	return RevealResultDTO{Value: val}
}

// ListInstances returns one page of instances of a kind plus the next token.
// Empty page on a cluster miss or error.
func (s *CRDService) ListInstances(cluster, group, version, plural, continueToken string) InstancePageDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return InstancePageDTO{Items: []InstanceDTO{}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), crdTimeout)
	defer cancel()
	items, next, err := conn.ListInstances(ctx, group, version, plural, instancePageSize, continueToken)
	if err != nil {
		return InstancePageDTO{Items: []InstanceDTO{}}
	}
	dtos := make([]InstanceDTO, 0, len(items))
	for _, m := range items {
		created := ""
		if !m.Created.IsZero() {
			created = m.Created.Format(time.RFC3339)
		}
		dtos = append(dtos, InstanceDTO{Namespace: m.Namespace, Name: m.Name, Created: created, Fields: m.Fields})
	}
	return InstancePageDTO{Items: dtos, NextToken: next}
}
