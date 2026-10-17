package appbridge

import (
	"context"
	"sort"
	"time"

	"github.com/moomora/klyx/internal/gitops/composition"
	"github.com/moomora/klyx/internal/gitops/flux"
	"github.com/moomora/klyx/internal/gitops/notify"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/urlscrub"
)

// Coverage S1c: the Sources view's composition rows and the Notifications
// view's Receiver rows (the Observer's shared "composition" and
// "receivers" parts), the on-demand Provider and Alert lists, and the
// inspectors' detail.

// CompositionCommonDTO is what a generator's and an artifact's rows carry.
type CompositionCommonDTO struct {
	UID             string     `json:"uid"`
	GVR             string     `json:"gvr"`
	Kind            string     `json:"kind"`
	Namespace       string     `json:"namespace"`
	Name            string     `json:"name"`
	Generation      int64      `json:"generation"`
	ReadyGeneration int64      `json:"readyGeneration,omitempty"`
	Created         *time.Time `json:"created,omitempty"`
	Ready           string     `json:"ready"`
	Reason          string     `json:"reason,omitempty"`
	Message         string     `json:"message,omitempty"`
	ReadySince      *time.Time `json:"readySince,omitempty"`
	Stalled         bool       `json:"stalled,omitempty"`
	Clipped         []string   `json:"clipped"`
	Incomplete      []string   `json:"incomplete"`
	Rule            RuleDTO    `json:"rule"`
}

// CompositionRefDTO is a joined object, with its verdict when read.
type CompositionRefDTO struct {
	Kind      string `json:"kind,omitempty"`
	Alias     string `json:"alias,omitempty"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	State     string `json:"state,omitempty"` // found | not found | incomplete | not readable
	Found     bool   `json:"found"`
	Label     string `json:"label,omitempty"`
	Role      string `json:"role,omitempty"`
}

// GeneratorOutputDTO is one artifact a generator declares, with its copy
// rules as text.
type GeneratorOutputDTO struct {
	Name     string   `json:"name"`
	Revision string   `json:"revision,omitempty"`
	Copy     []string `json:"copy"`
}

// GeneratorDTO is an ArtifactGenerator row.
type GeneratorDTO struct {
	CompositionCommonDTO
	Suspended         bool                 `json:"suspended"`
	Resume            string               `json:"resume,omitempty"`
	RequestedAt       string               `json:"requestedAt,omitempty"`
	LastHandled       string               `json:"lastHandled,omitempty"`
	Sources           []CompositionRefDTO  `json:"sources"`
	SourcesIncomplete bool                 `json:"sourcesIncomplete,omitempty"`
	Outputs           []GeneratorOutputDTO `json:"outputs"`
	Generated         []CompositionRefDTO  `json:"generated"`
	GeneratedState    string               `json:"generatedState"`
}

// ArtifactSourceRefDTO is an artifact's declared producer reference.
type ArtifactSourceRefDTO struct {
	APIVersion string `json:"apiVersion,omitempty"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
}

// ArtifactDTO is an ExternalArtifact row: its own verdict, then its
// producer as a separate block (S1c §2).
type ArtifactDTO struct {
	CompositionCommonDTO
	HasArtifact    bool                  `json:"hasArtifact"`
	Revision       string                `json:"revision,omitempty"`
	Digest         string                `json:"digest,omitempty"`
	Origin         string                `json:"origin,omitempty"` // the URL's scheme and host only
	OriginRevision string                `json:"originRevision,omitempty"`
	LastUpdate     *time.Time            `json:"lastUpdate,omitempty"`
	Size           int64                 `json:"size,omitempty"`
	SourceRef      *ArtifactSourceRefDTO `json:"sourceRef,omitempty"`
	ManagedBy      string                `json:"managedBy,omitempty"`
	Producer       *SourceProducerDTO    `json:"producer,omitempty"`
}

// CompositionRowDTO is one row of the composition part: exactly one kind.
type CompositionRowDTO struct {
	UID       string        `json:"uid"`
	Kind      string        `json:"kind"`
	Rule      RuleDTO       `json:"rule"`
	Generator *GeneratorDTO `json:"generator,omitempty"`
	Artifact  *ArtifactDTO  `json:"artifact,omitempty"`
}

func compositionCommon(c composition.Common, ref healthsrc.RowRef, eval RuleDTO) CompositionCommonDTO {
	return CompositionCommonDTO{UID: ref.UID, GVR: ref.GVR, Kind: c.Kind, Namespace: c.Namespace, Name: c.Name, Generation: c.Generation,
		ReadyGeneration: c.ReadyGeneration, Created: timePtrOf(c.Created), Ready: string(c.Ready), Reason: c.Reason, Message: c.Message,
		ReadySince: timePtrOf(c.ReadySince), Stalled: c.Stalled, Clipped: nonNil(c.Clipped), Incomplete: nonNil(c.Incomplete), Rule: eval}
}

func compositionRowDTO(r healthsrc.Row[healthsrc.CompositionRow]) CompositionRowDTO {
	eval := toRuleDTO(r.Eval)
	out := CompositionRowDTO{UID: r.Ref.UID, Rule: eval}
	switch {
	case r.Data.Generator != nil:
		g := r.Data.Generator
		d := GeneratorDTO{CompositionCommonDTO: compositionCommon(g.Common, r.Ref, eval), Suspended: g.Disabled, Resume: r.Data.Resume,
			RequestedAt: g.RequestedAt, LastHandled: g.LastHandled, Sources: []CompositionRefDTO{}, SourcesIncomplete: g.SourcesIncomplete,
			Outputs: []GeneratorOutputDTO{}, Generated: []CompositionRefDTO{}, GeneratedState: r.Data.GeneratedState}
		for _, s := range g.Sources {
			d.Sources = append(d.Sources, CompositionRefDTO{Kind: s.Kind, Alias: s.Alias, Namespace: s.Namespace, Name: s.Name})
		}
		for _, o := range g.Outputs {
			d.Outputs = append(d.Outputs, GeneratorOutputDTO{Name: o.Name, Revision: o.Revision, Copy: nonNil(o.Copy)})
		}
		for _, a := range r.Data.Generated {
			d.Generated = append(d.Generated, CompositionRefDTO{Kind: composition.KindArtifact, Namespace: a.Namespace, Name: a.Name, Found: a.Found, Label: a.Label, Role: a.Role})
		}
		out.Kind, out.Generator = composition.KindGenerator, &d
	case r.Data.Artifact != nil:
		a := r.Data.Artifact
		d := ArtifactDTO{CompositionCommonDTO: compositionCommon(a.Common, r.Ref, eval), HasArtifact: a.HasArtifact, Revision: a.Revision,
			Digest: a.Digest, Origin: a.Origin, OriginRevision: a.OriginRevision, LastUpdate: timePtrOf(a.LastUpdate), Size: a.Size, ManagedBy: a.ManagedBy}
		if s := a.SourceRef; s != nil {
			d.SourceRef = &ArtifactSourceRefDTO{APIVersion: s.APIVersion, Kind: s.Kind, Namespace: s.Namespace, Name: s.Name}
		}
		if p := r.Data.Producer; p != nil {
			d.Producer = sourceProducerDTO(p)
		}
		out.Kind, out.Artifact = composition.KindArtifact, &d
	}
	return out
}

// ReceiverResourceDTO is one object a Receiver reconciles on a webhook.
type ReceiverResourceDTO struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Namespace   string `json:"namespace,omitempty"`
	MatchLabels int    `json:"matchLabels,omitempty"`
}

// ReceiverDTO is a Receiver row: never its webhook path or Ready message.
type ReceiverDTO struct {
	UID                string                `json:"uid"`
	GVR                string                `json:"gvr"`
	Kind               string                `json:"kind"`
	Namespace          string                `json:"namespace"`
	Name               string                `json:"name"`
	Generation         int64                 `json:"generation"`
	ObservedGeneration int64                 `json:"observedGeneration,omitempty"`
	Created            *time.Time            `json:"created,omitempty"`
	Type               string                `json:"type,omitempty"`
	Interval           string                `json:"interval,omitempty"`
	SecretRef          string                `json:"secretRef,omitempty"`
	Events             []string              `json:"events"`
	Resources          []ReceiverResourceDTO `json:"resources"`
	ResourceCount      int                   `json:"resourceCount"`
	FilterCount        int                   `json:"filterCount"`
	ResourceFilter     bool                  `json:"resourceFilter,omitempty"`
	OIDCProviders      int                   `json:"oidcProviders,omitempty"`
	Suspended          bool                  `json:"suspended"`
	Ready              string                `json:"ready"`
	Reason             string                `json:"reason,omitempty"`
	ReadySince         *time.Time            `json:"readySince,omitempty"`
	ReadyGeneration    int64                 `json:"readyGeneration,omitempty"`
	Stalled            bool                  `json:"stalled,omitempty"`
	RequestedAt        string                `json:"requestedAt,omitempty"`
	Owner              *FluxOpOwnerDTO       `json:"owner,omitempty"`
	Meaning            string                `json:"meaning,omitempty"` // what its Ready means
	Clipped            []string              `json:"clipped"`
	Rule               RuleDTO               `json:"rule"`
}

func receiverRowDTO(r healthsrc.Row[healthsrc.ReceiverRow]) ReceiverDTO {
	x := r.Data
	d := ReceiverDTO{UID: r.Ref.UID, GVR: r.Ref.GVR, Kind: notify.KindReceiver, Namespace: x.Namespace, Name: x.Name, Generation: x.Generation,
		ObservedGeneration: x.ObservedGeneration, Created: timePtrOf(x.Created), Type: x.Type, Interval: x.Interval, SecretRef: x.SecretRef,
		Events: nonNil(x.Events), Resources: []ReceiverResourceDTO{}, ResourceCount: x.ResourceCount, FilterCount: x.FilterCount,
		ResourceFilter: x.ResourceFilter, OIDCProviders: x.OIDCProviders, Suspended: x.Suspended, Ready: x.Ready, Reason: x.Reason,
		ReadySince: timePtrOf(x.ReadySince), ReadyGeneration: x.ReadyGeneration, Stalled: x.Stalled, RequestedAt: x.RequestedAt,
		Meaning: x.Meaning, Clipped: nonNil(x.Clipped), Rule: toRuleDTO(r.Eval)}
	for _, res := range x.Resources {
		d.Resources = append(d.Resources, ReceiverResourceDTO{Kind: res.Kind, Name: res.Name, Namespace: res.Namespace, MatchLabels: res.MatchLabels})
	}
	if x.OwnerKind != "" {
		d.Owner = &FluxOpOwnerDTO{Kind: x.OwnerKind, Namespace: x.OwnerNamespace, Name: x.OwnerName}
	}
	return d
}

func init() {
	rowsConverters[healthsrc.TaskFlux+"/composition"] = func(rows any, _ time.Time) (any, error) {
		set, err := rowSetOf[healthsrc.CompositionRow](rows)
		if err != nil {
			return nil, err
		}
		out := make([]CompositionRowDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			out = append(out, compositionRowDTO(r))
		}
		return out, nil
	}
	rowsConverters[healthsrc.TaskFlux+"/receivers"] = func(rows any, _ time.Time) (any, error) {
		set, err := rowSetOf[healthsrc.ReceiverRow](rows)
		if err != nil {
			return nil, err
		}
		out := make([]ReceiverDTO, 0, len(set.Rows))
		for _, r := range set.Rows {
			out = append(out, receiverRowDTO(r))
		}
		return out, nil
	}
}

// ---- Providers and Alerts, on demand -------------------------------------

// ProviderDTO is a Provider's facts (S1c §2): never its address or proxy,
// only whether they are set; the Secrets it names, never read.
type ProviderDTO struct {
	UID                string              `json:"uid"`
	Namespace          string              `json:"namespace"`
	Name               string              `json:"name"`
	Type               string              `json:"type,omitempty"`
	Channel            string              `json:"channel,omitempty"`
	HasAddress         bool                `json:"hasAddress"`
	HasProxy           bool                `json:"hasProxy"`
	SecretRef          string              `json:"secretRef,omitempty"`
	CertSecretRef      string              `json:"certSecretRef,omitempty"`
	ProxySecretRef     string              `json:"proxySecretRef,omitempty"`
	ServiceAccountName string              `json:"serviceAccountName,omitempty"`
	Interval           string              `json:"interval,omitempty"`
	Timeout            string              `json:"timeout,omitempty"`
	Suspended          bool                `json:"suspended"`
	Owner              *FluxOpOwnerDTO     `json:"owner,omitempty"`
	Alerts             []CompositionRefDTO `json:"alerts"`
	AlertsState        string              `json:"alertsState"` // found | incomplete | not readable
}

// AlertEventSourceDTO is one source an Alert listens to.
type AlertEventSourceDTO struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Namespace   string `json:"namespace,omitempty"`
	MatchLabels int    `json:"matchLabels,omitempty"`
}

// AlertDTO is an Alert's facts and its dispatch evidence: a count and the
// reasons from its recent events, never a verdict or a message.
type AlertDTO struct {
	UID               string                `json:"uid"`
	Namespace         string                `json:"namespace"`
	Name              string                `json:"name"`
	ProviderName      string                `json:"providerName"`
	ProviderState     string                `json:"providerState"` // found | not found | incomplete | not readable
	ProviderSuspended bool                  `json:"providerSuspended,omitempty"`
	EventSeverity     string                `json:"eventSeverity,omitempty"`
	EventSources      []AlertEventSourceDTO `json:"eventSources"`
	InclusionList     []string              `json:"inclusionList"`
	ExclusionList     []string              `json:"exclusionList"`
	MetadataKeys      []string              `json:"metadataKeys"`
	Suspended         bool                  `json:"suspended"`
	Owner             *FluxOpOwnerDTO       `json:"owner,omitempty"`
	Dispatch          map[string]int        `json:"dispatch"`
	DispatchSummary   string                `json:"dispatchSummary"`
}

// NotificationListDTO is one capped list's state.
type NotificationListDTO struct {
	Absent bool   `json:"absent,omitempty"`
	Error  string `json:"error,omitempty"`
	// More: how many are past the cap; -1 when the server does not say.
	More int64 `json:"more,omitempty"`
}

// NotificationsDTO is the Notifications view's on-demand read.
type NotificationsDTO struct {
	Providers       []ProviderDTO       `json:"providers"`
	Alerts          []AlertDTO          `json:"alerts"`
	ProvidersList   NotificationListDTO `json:"providersList"`
	AlertsList      NotificationListDTO `json:"alertsList"`
	EventsError     string              `json:"eventsError,omitempty"`
	EventsTruncated bool                `json:"eventsTruncated,omitempty"`
	AsOfUnix        int64               `json:"asOfUnix"`
	Error           string              `json:"error,omitempty"`
}

func listState(l notify.List) NotificationListDTO {
	return NotificationListDTO{Absent: l.Absent, Error: l.Error, More: l.More}
}

func ownerOf(kind, ns, name string) *FluxOpOwnerDTO {
	if kind == "" {
		return nil
	}
	return &FluxOpOwnerDTO{Kind: kind, Namespace: ns, Name: name}
}

func scrubbed(xs []string) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		out = append(out, urlscrub.Text(x).Text)
	}
	return out
}

// GetNotifications reads the Providers and Alerts (S1c §3), joins each
// Alert to its Provider and each Provider to the Alerts using it, and
// counts the Alerts' dispatch evidence. Nothing is retained.
func (s *GitOpsService) GetNotifications(cluster string) NotificationsDTO {
	out := NotificationsDTO{Providers: []ProviderDTO{}, Alerts: []AlertDTO{}}
	conn, ok := s.lookup(cluster)
	if !ok {
		out.Error = "cluster not connected: " + cluster
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	r := conn.FluxNotifications(ctx)
	out.ProvidersList, out.AlertsList = listState(r.Providers), listState(r.Alerts)
	out.EventsError, out.EventsTruncated, out.AsOfUnix = r.EventsError, r.EventsTruncated, s.now().Unix()
	var providers []notify.Provider
	for _, u := range r.Providers.Items {
		providers = append(providers, notify.ParseProvider(u))
	}
	var alerts []notify.Alert
	for _, u := range r.Alerts.Items {
		alerts = append(alerts, notify.ParseAlert(u))
	}
	alertsState := notify.ProviderFound
	switch {
	case r.Alerts.Error != "":
		alertsState = joinUnreadable
	case r.Alerts.More != 0:
		alertsState = notify.ProviderIncomplete
	}
	for _, p := range providers {
		d := ProviderDTO{UID: p.UID, Namespace: p.Namespace, Name: p.Name, Type: p.Type, Channel: p.Channel, HasAddress: p.HasAddress,
			HasProxy: p.HasProxy, SecretRef: p.SecretRef, CertSecretRef: p.CertSecretRef, ProxySecretRef: p.ProxySecretRef,
			ServiceAccountName: p.ServiceAccountName, Interval: p.Interval, Timeout: p.Timeout, Suspended: p.Suspended,
			Owner: ownerOf(p.OwnerKind, p.OwnerNamespace, p.OwnerName), Alerts: []CompositionRefDTO{}, AlertsState: alertsState}
		for _, a := range alerts {
			if a.Namespace == p.Namespace && a.ProviderName == p.Name {
				d.Alerts = append(d.Alerts, CompositionRefDTO{Kind: notify.KindAlert, Namespace: a.Namespace, Name: a.Name, Found: true})
			}
		}
		out.Providers = append(out.Providers, d)
	}
	for _, a := range alerts {
		d := AlertDTO{UID: a.UID, Namespace: a.Namespace, Name: a.Name, ProviderName: a.ProviderName, EventSeverity: a.EventSeverity,
			EventSources: []AlertEventSourceDTO{}, InclusionList: scrubbed(a.InclusionList), ExclusionList: scrubbed(a.ExclusionList),
			MetadataKeys: nonNil(a.MetadataKeys), Suspended: a.Suspended, Owner: ownerOf(a.OwnerKind, a.OwnerNamespace, a.OwnerName)}
		if r.Providers.Error != "" {
			d.ProviderState = joinUnreadable
		} else {
			l := notify.AlertProvider(a, providers, r.Providers.Complete())
			d.ProviderState, d.ProviderSuspended = l.State, l.Suspended
		}
		for _, src := range a.EventSources {
			d.EventSources = append(d.EventSources, AlertEventSourceDTO{Kind: src.Kind, Name: src.Name, Namespace: src.Namespace, MatchLabels: src.MatchLabels})
		}
		d.Dispatch = notify.Dispatch(r.AlertReasons[a.Namespace+"/"+a.Name])
		d.DispatchSummary = notify.SummaryOf(d.Dispatch)
		if r.EventsError != "" {
			d.DispatchSummary = "dispatch evidence could not be read: the events read failed"
		}
		out.Alerts = append(out.Alerts, d)
	}
	sort.Slice(out.Providers, func(i, j int) bool {
		return out.Providers[i].Namespace+"/"+out.Providers[i].Name < out.Providers[j].Namespace+"/"+out.Providers[j].Name
	})
	sort.Slice(out.Alerts, func(i, j int) bool {
		return out.Alerts[i].Namespace+"/"+out.Alerts[i].Name < out.Alerts[j].Namespace+"/"+out.Alerts[j].Name
	})
	return out
}

// joinUnreadable is the state of a join whose read failed.
const joinUnreadable = "not readable"

// ---- the inspectors' detail ----------------------------------------------

// CompositionDetailDTO is the on-demand detail of a generator, an artifact
// or a Receiver, read for the shown object only (its UID).
type CompositionDetailDTO struct {
	UID      string `json:"uid,omitempty"`
	Replaced bool   `json:"replaced,omitempty"`
	Error    string `json:"error,omitempty"`
	AsOfUnix int64  `json:"asOfUnix,omitempty"`
	// a generator's declared sources, with their verdicts from the shared
	// rows; an artifact's consumers
	Sources        []CompositionRefDTO `json:"sources"`
	SourcesState   string              `json:"sourcesState,omitempty"`
	Consumers      []CompositionRefDTO `json:"consumers"`
	ConsumersState string              `json:"consumersState,omitempty"`
	Events         []EventRowDTO       `json:"events"`
	EventsError    string              `json:"eventsError,omitempty"`
}

// GetCompositionDetail reads an ArtifactGenerator's, ExternalArtifact's or
// Receiver's detail: the object is read to check it is the one shown (a
// replacement is said, and nothing else is read); its events under Klyx's
// message rules; a generator's sources and an artifact's consumers from
// the shared rows.
func (s *GitOpsService) GetCompositionDetail(cluster, kind, namespace, name, uid string) CompositionDetailDTO {
	out := CompositionDetailDTO{Sources: []CompositionRefDTO{}, Consumers: []CompositionRefDTO{}, Events: []EventRowDTO{}}
	switch kind {
	case composition.KindGenerator, composition.KindArtifact, notify.KindReceiver:
	default:
		out.Error = kind + " has no composition detail"
		return out
	}
	if uid == "" {
		out.Error = "Klyx does not know which " + kind + " is shown"
		return out
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		out.Error = "cluster not connected: " + cluster
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	u, err := conn.FluxObject(ctx, kind, namespace, name)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	if string(u.GetUID()) != uid {
		out.Replaced = true
		return out
	}
	out.UID, out.AsOfUnix = uid, s.now().Unix()
	if evs, err := conn.FluxEvents(ctx, kind, namespace, name); err != nil {
		out.EventsError = err.Error()
	} else {
		for _, e := range evs {
			out.Events = append(out.Events, toEventRowDTO(e))
		}
	}
	switch kind {
	case composition.KindGenerator:
		if v, err := composition.Project(composition.KindGenerator)(u); err == nil {
			out.Sources, out.SourcesState = s.generatorSources(ctx, conn, composition.ParseGenerator(v.(*composition.Object)))
		}
	case composition.KindArtifact:
		out.Consumers, out.ConsumersState = s.artifactConsumers(ctx, conn, namespace, name)
	}
	return out
}

// rowsState is a part's read state for a join: found when live and
// complete, incomplete when a kind is capped, not readable otherwise.
func rowsState(rows observe.Rows, ok bool) string {
	switch {
	case !ok || rows.Part.Coverage != observe.Live:
		return joinUnreadable
	}
	for _, k := range rows.Part.Kinds {
		if k.Truncated {
			return notify.ProviderIncomplete
		}
	}
	return notify.ProviderFound
}

// generatorSources joins a generator's declared sources to their verdicts:
// Flux sources from the "sources" rows, artifacts from the composition rows.
func (s *GitOpsService) generatorSources(ctx context.Context, conn GitOpsConn, g composition.Generator) ([]CompositionRefDTO, string) {
	src, srcOK := conn.HealthRowsNow(ctx, healthsrc.TaskFlux, "sources")
	comp, compOK := conn.HealthRowsNow(ctx, healthsrc.TaskFlux, "composition")
	srcSet, _ := src.Rows.(healthsrc.RowSet[flux.Source])
	compSet, _ := comp.Rows.(healthsrc.RowSet[healthsrc.CompositionRow])
	worst := notify.ProviderFound
	out := []CompositionRefDTO{}
	for _, ref := range g.Sources {
		d := CompositionRefDTO{Kind: ref.Kind, Alias: ref.Alias, Namespace: ref.Namespace, Name: ref.Name}
		state := rowsState(src, srcOK)
		if ref.Kind == composition.KindArtifact {
			state = rowsState(comp, compOK)
			for _, r := range compSet.Rows {
				if a := r.Data.Artifact; a != nil && a.Namespace == ref.Namespace && a.Name == ref.Name {
					d.Found, d.Label, d.Role = true, r.Eval.Label, r.Eval.Role.String()
				}
			}
		} else {
			for _, r := range srcSet.Rows {
				if string(r.Data.Kind) == ref.Kind && r.Data.Namespace == ref.Namespace && r.Data.Name == ref.Name {
					d.Found, d.Label, d.Role = true, r.Eval.Label, r.Eval.Role.String()
				}
			}
		}
		switch {
		case d.Found:
			d.State = notify.ProviderFound
		case state == notify.ProviderFound:
			d.State = notify.ProviderMissing
		default:
			d.State = state
		}
		if state != notify.ProviderFound {
			worst = state
		}
		out = append(out, d)
	}
	return out, worst
}

// artifactConsumers lists the Kustomizations and HelmReleases bound to an
// artifact, with their verdicts, from the reconciliations rows.
func (s *GitOpsService) artifactConsumers(ctx context.Context, conn GitOpsConn, ns, name string) ([]CompositionRefDTO, string) {
	rows, ok := conn.HealthRowsNow(ctx, healthsrc.TaskFlux, "reconciliations")
	state := rowsState(rows, ok)
	set, _ := rows.Rows.(healthsrc.RowSet[flux.Resource])
	out := []CompositionRefDTO{}
	for _, r := range set.Rows {
		k := r.Data
		if k.SourceKind == composition.KindArtifact && k.SourceNamespace == ns && k.SourceName == name {
			out = append(out, CompositionRefDTO{Kind: string(k.Kind), Namespace: k.Namespace, Name: k.Name, Found: true, Label: r.Eval.Label, Role: r.Eval.Role.String()})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Kind+out[i].Namespace+out[i].Name < out[j].Kind+out[j].Namespace+out[j].Name
	})
	return out, state
}
