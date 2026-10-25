package appbridge

import (
	"context"
	"errors"
	"github.com/moomora/klyx/internal/urlscrub"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/moomora/klyx/internal/crd"
	"github.com/moomora/klyx/internal/fleet"
)

// CRDConn is the per-cluster read surface CRDService needs.
type CRDConn interface {
	ListCRDs(ctx context.Context) ([]crd.Info, error)
	ListCRDsFromDiscovery(ctx context.Context) ([]crd.Info, error)
	GetCRDDefinition(ctx context.Context, name, version string) (crd.Definition, error)
	CountResource(ctx context.Context, group, version, plural string) (int, bool, error)
	ListInstances(ctx context.Context, group, version, plural string, limit int64, continueToken string) (crd.InstancePage, error)
	GetInstanceDetail(ctx context.Context, group, version, plural, ns, name string) (crd.InstanceDetail, error)
	ApplyResourceYAML(ctx context.Context, group, version, plural, kind, scope, ns, name, expectedUID, yaml string, dryRun bool) error
	CreateResourceYAML(ctx context.Context, yamlDocs string, dryRun bool) []fleet.AdhocResult
	ListAdhocObjects(ctx context.Context) (fleet.AdhocInventory, error)
	DeleteAdhocObject(ctx context.Context, group, version, plural, ns, name, uid string) error
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
// sorted deterministically. A cluster miss or list error returns empty Groups
// WITH Error - never a silent "no CRDs".
func (s *CRDService) ListCRDs(cluster string) CRDListDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return CRDListDTO{Groups: []CRDGroupDTO{}, Error: "cluster not connected: " + cluster}
	}
	ctx, cancel := context.WithTimeout(context.Background(), crdTimeout)
	defer cancel()
	infos, err := conn.ListCRDs(ctx)
	if err != nil {
		// The discovery fallback (The Line slice 15c): the kinds still open
		// from discovery, under the definitions' own failure - never "no CRDs".
		found, derr := conn.ListCRDsFromDiscovery(ctx)
		var partial *fleet.PartialDiscoveryError
		switch {
		case derr == nil:
			return CRDListDTO{Groups: groupCRDs(found), Fallback: err.Error()}
		case errors.As(derr, &partial):
			// the groups that were read still list; the missing ones are named
			return CRDListDTO{Groups: groupCRDs(found), Fallback: err.Error(), FallbackMissing: partial.Failed}
		}
		return CRDListDTO{Groups: []CRDGroupDTO{}, Error: err.Error()}
	}
	return CRDListDTO{Groups: groupCRDs(infos)}
}

// GetCRDDefinition reads one CRD in full with the schema of version ("" for
// the storage version). A failed read carries Error, never an empty CRD.
func (s *CRDService) GetCRDDefinition(cluster, name, version string) CRDDefinitionDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return CRDDefinitionDTO{Error: "cluster not connected: " + cluster}
	}
	ctx, cancel := context.WithTimeout(context.Background(), crdTimeout)
	defer cancel()
	d, err := conn.GetCRDDefinition(ctx, name, version)
	if err != nil {
		return CRDDefinitionDTO{Error: err.Error(), NotFound: apierrors.IsNotFound(err)}
	}
	return definitionDTO(d)
}

// CountKind returns the hybrid instance count for one kind. A cluster miss or
// count error carries Error - a forbidden count is not a zero.
func (s *CRDService) CountKind(cluster, group, version, plural string) CRDCountDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return CRDCountDTO{Error: "cluster not connected: " + cluster}
	}
	ctx, cancel := context.WithTimeout(context.Background(), crdTimeout)
	defer cancel()
	count, capped, err := conn.CountResource(ctx, group, version, plural)
	if err != nil {
		return CRDCountDTO{Error: err.Error()}
	}
	return CRDCountDTO{Count: count, Capped: capped}
}

func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// GetInstanceDetail returns the full per-instance detail. A cluster miss or
// read error returns the zero DTO WITH Error, never a blank object.
func (s *CRDService) GetInstanceDetail(cluster, group, version, plural, namespace, name string) InstanceDetailDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return InstanceDetailDTO{Error: "cluster not connected: " + cluster}
	}
	ctx, cancel := context.WithTimeout(context.Background(), crdTimeout)
	defer cancel()
	d, err := conn.GetInstanceDetail(ctx, group, version, plural, namespace, name)
	if err != nil {
		return InstanceDetailDTO{Error: err.Error(), NotFound: apierrors.IsNotFound(err)}
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
		conds = append(conds, ConditionDTO{Type: c.Type, Status: c.Status, Reason: c.Reason, Message: c.Message, Scope: c.Scope,
			LastTransition: rfc3339(c.LastTransition), ObservedGeneration: c.ObservedGeneration})
	}
	facts := make([]FactDTO, 0, len(d.Facts))
	for _, f := range d.Facts {
		facts = append(facts, FactDTO{Group: f.Group, Label: f.Label, Value: f.Value, Tone: f.Tone, Kind: f.Kind, Pct: f.Pct})
	}
	events := make([]EventDTO, 0, len(d.Events))
	for _, e := range d.Events {
		events = append(events, EventDTO{Type: e.Type, Reason: e.Reason, Message: urlscrub.Text(e.Message).Text, Count: int(e.Count), LastSeen: rfc3339(e.Last)})
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
		Kind: d.Kind, Namespace: d.Namespace, Name: d.Name, UID: d.UID, Generation: d.Generation,
		Created: rfc3339(d.Created), Labels: labels, Annotations: annotations,
		Conditions: conds, Events: events, YAML: d.YAML,
		SecretKeys:     secretKeys,
		ServiceBacking: serviceBacking,
		HPAScaling:     hpaScaling,
		Related:        related,
		Facts:          facts,

		Summary:            summaryDTOs(d.Summary),
		ObservedGeneration: d.ObservedGeneration,
		Deleting:           rfc3339(d.Deleting),
		Finalizers:         d.Finalizers,
		Owners:             ownerDTOs(d.Owners),
		Managers:           managerDTOs(d.Managers),
		FieldsJSON:         d.FieldsJSON,
		FieldsOmitted:      d.FieldsOmitted,
		EditEligible:       d.EditEligible,
		DescribeMode:       d.DescribeMode,
	}
}

func summaryDTOs(cells []crd.SummaryCell) []SummaryCellDTO {
	if cells == nil {
		return nil
	}
	cols := make([]crd.Column, 0, len(cells))
	for _, c := range cells {
		cols = append(cols, c.Column)
	}
	colDTOs := columnDTOs(cols)
	out := make([]SummaryCellDTO, 0, len(cells))
	for i, c := range cells {
		out = append(out, SummaryCellDTO{InstanceColumnDTO: colDTOs[i], Value: c.Value})
	}
	return out
}

func ownerDTOs(os []crd.OwnerRef) []OwnerRefDTO {
	out := make([]OwnerRefDTO, 0, len(os))
	for _, o := range os {
		out = append(out, OwnerRefDTO{APIVersion: o.APIVersion, Kind: o.Kind, Name: o.Name, Controller: o.Controller})
	}
	return out
}

func managerDTOs(ms []crd.FieldManager) []FieldManagerDTO {
	out := make([]FieldManagerDTO, 0, len(ms))
	for _, m := range ms {
		out = append(out, FieldManagerDTO{Manager: m.Manager, Operation: m.Operation, Subresource: m.Subresource, Time: rfc3339(m.Time)})
	}
	return out
}

// ApplyResourceYAML validates and server-side-applies the YAML for the resource
// currently open in the generic resource detail. dryRun=true asks the apiserver
// to validate/admit without persisting.
func (s *CRDService) ApplyResourceYAML(cluster, group, version, plural, kind, scope, namespace, name, expectedUID, yaml string, dryRun bool) ActionResultDTO {
	// Dry-run mutates nothing; only a real apply consumes the protection grant
	// and writes under its ticket.
	base := context.Background() // unfenced: a dry run writes nothing
	if !dryRun {
		t, err := guardFor(cluster)
		if err != nil {
			return ActionResultDTO{Error: err.Error()}
		}
		base = t.Write(base)
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		return ActionResultDTO{Error: "cluster not connected: " + cluster}
	}
	ctx, cancel := context.WithTimeout(base, crdTimeout)
	defer cancel()
	if err := conn.ApplyResourceYAML(ctx, group, version, plural, kind, scope, namespace, name, expectedUID, yaml, dryRun); err != nil {
		r := failed(ctx, conn, err)
		// a protected subject keeps its withheld form, its paths included
		if !protectedSubject(group, kind) {
			r.Conflict = conflictDTO(err)
		}
		return r
	}
	return ActionResultDTO{OK: true}
}

// CreateResourceYAML creates the documents in yaml as NEW ad-hoc objects (the
// deliberately-non-GitOps scratch path: stamped klyx.dev/adhoc, plain create -
// name collisions refuse, Secrets refused, explicit namespaces required). A
// real create on a protected cluster consumes the one-shot typed-confirmation
// grant, exactly like ApplyResourceYAML; dry-run never does.
func (s *CRDService) CreateResourceYAML(cluster, yaml string, dryRun bool) []AdhocResultDTO {
	base := context.Background() // unfenced: a dry run writes nothing
	if !dryRun {
		t, err := guardFor(cluster)
		if err != nil {
			return []AdhocResultDTO{{Error: err.Error()}}
		}
		// one ticket; each document's create gets its own forwarding
		// tracker (fleet.CreateResourceYAML)
		base = t.Write(base)
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		return []AdhocResultDTO{{Error: "cluster not connected: " + cluster}}
	}
	ctx, cancel := context.WithTimeout(base, crdTimeout)
	defer cancel()
	rs := conn.CreateResourceYAML(ctx, yaml, dryRun)
	out := make([]AdhocResultDTO, 0, len(rs))
	for _, r := range rs {
		text, f := adhocFinding(ctx, conn, r.Error, r.Refusal())
		out = append(out, AdhocResultDTO{
			Kind: r.Kind, Namespace: r.Namespace, Name: r.Name, OK: r.OK, Error: text, Admission: f,
			NamespaceOwnerKind: r.NamespaceOwnerKind, NamespaceOwnerName: r.NamespaceOwnerName,
			NamespaceOwnerNamespace: r.NamespaceOwnerNamespace, NamespacePrunes: r.NamespacePrunes,
			NamespaceManaged: r.NamespaceManaged, ArgoTracking: r.ArgoTracking, ArgoTrackingApp: r.ArgoTrackingApp,
			ArgoTrackingPrunes: r.ArgoTrackingPrunes, ArgoPruneOption: r.ArgoPruneOption, ArgoConfigKnown: r.ArgoConfigKnown,
		})
	}
	return out
}

// ListAdhocObjects returns everything Klyx created ad-hoc on the cluster (the
// scratch inventory: label klyx.dev/adhoc), the kinds that could not be read,
// and an error when the sweep itself failed. Read-only.
func (s *CRDService) ListAdhocObjects(cluster string) AdhocInventoryDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return AdhocInventoryDTO{Objects: []AdhocObjectDTO{}, Unread: []string{}, Error: "cluster not connected: " + cluster}
	}
	ctx, cancel := context.WithTimeout(context.Background(), crdTimeout)
	defer cancel()
	inv, err := conn.ListAdhocObjects(ctx)
	if err != nil {
		return AdhocInventoryDTO{Objects: []AdhocObjectDTO{}, Unread: []string{}, Error: err.Error()}
	}
	out := AdhocInventoryDTO{Objects: make([]AdhocObjectDTO, 0, len(inv.Objects)), Unread: append([]string{}, inv.Unread...)}
	for _, o := range inv.Objects {
		out.Objects = append(out.Objects, AdhocObjectDTO{
			Kind: o.Kind, Group: o.Group, Version: o.Version, Plural: o.Plural,
			Scope: o.Scope, Namespace: o.Namespace, Name: o.Name, UID: o.UID, AgeSeconds: o.AgeSeconds,
		})
	}
	return out
}

// DeleteAdhocObject deletes one Klyx-created scratch object. Label-guarded and
// pinned to the listed UID in the data layer (never deletes anything Klyx did
// not create, nor a replacement under the same name) and consumes the
// protected-cluster grant like every destructive verb.
func (s *CRDService) DeleteAdhocObject(cluster, group, version, plural, namespace, name, uid string) ActionResultDTO {
	t, err := guardFor(cluster)
	if err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		return ActionResultDTO{Error: "cluster not connected: " + cluster}
	}
	ctx, cancel := context.WithTimeout(t.Write(context.Background()), crdTimeout)
	defer cancel()
	if err := conn.DeleteAdhocObject(ctx, group, version, plural, namespace, name, uid); err != nil {
		return failed(ctx, conn, err)
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
		NamespaceManaged: o.NamespaceManaged, NamespaceBasis: o.NamespaceBasis,
		ArgoApps: make([]ArgoAppRefDTO, 0, len(o.ArgoApps)),
		Warnings: []string{},
	}
	for _, a := range o.ArgoApps {
		dto.ArgoApps = append(dto.ArgoApps, ArgoAppRefDTO{Namespace: a.Namespace, Name: a.Name, Prunes: a.Prunes})
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
// A cluster miss or list error returns an empty page WITH Error.
func (s *CRDService) ListInstances(cluster, group, version, plural, continueToken string) InstancePageDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return InstancePageDTO{Items: []InstanceDTO{}, Error: "cluster not connected: " + cluster}
	}
	ctx, cancel := context.WithTimeout(context.Background(), crdTimeout)
	defer cancel()
	page, err := conn.ListInstances(ctx, group, version, plural, instancePageSize, continueToken)
	if err != nil {
		// A cluster-wide list only 404s when the resource itself is not served
		// (e.g. a CRD that is not installed): that is a definitive zero, not an
		// unreadable read.
		if apierrors.IsNotFound(err) {
			return InstancePageDTO{Items: []InstanceDTO{}, NotServed: true}
		}
		return InstancePageDTO{Items: []InstanceDTO{}, Error: err.Error()}
	}
	dtos := make([]InstanceDTO, 0, len(page.Items))
	for _, m := range page.Items {
		created := ""
		if !m.Created.IsZero() {
			created = m.Created.Format(time.RFC3339)
		}
		dtos = append(dtos, InstanceDTO{Namespace: m.Namespace, Name: m.Name, Created: created, Fields: m.Fields, Cells: m.Cells})
	}
	return InstancePageDTO{Items: dtos, NextToken: page.Continue, Columns: columnDTOs(page.Columns)}
}

// columnDTOs carries the server's printer columns; nil for a page read
// without a table.
func columnDTOs(cols []crd.Column) []InstanceColumnDTO {
	if cols == nil {
		return nil
	}
	out := make([]InstanceColumnDTO, 0, len(cols))
	for _, c := range cols {
		out = append(out, InstanceColumnDTO{Name: c.Name, Type: c.Type, Format: c.Format, Description: c.Description, JSONPath: c.JSONPath, Priority: c.Priority, Numeric: c.Numeric, ConditionType: c.ConditionType})
	}
	return out
}
