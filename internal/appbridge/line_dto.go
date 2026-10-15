package appbridge

import (
	"sort"
	"time"

	"github.com/moomora/klyx/internal/line"
)

// LineEvent carries the whole line (The Line slice 3 note §3). A consumer
// keeps a DTO only when its version is newer than the one it holds.
const LineEvent = "line:dto"

// LineDTO is the board's data.
type LineDTO struct {
	Version     uint64            `json:"version"`
	Key         *LineKeyDTO       `json:"key,omitempty"`
	KeyHow      string            `json:"keyHow,omitempty"` // most stations | override | selector
	KeyStations int               `json:"keyStations"`
	Keys        []LineKeyCountDTO `json:"keys"`
	R           string            `json:"r,omitempty"`
	RStations   int               `json:"rStations"`
	RTieBroken  bool              `json:"rTieBroken,omitempty"`
	RTieBy      string            `json:"rTieBy,omitempty"` // "fetch time" (not commit order) | "lexical version string" (not version recency)
	Applied     int               `json:"applied"`
	Compared    int               `json:"compared"`
	NotCompared int               `json:"notCompared"`
	Checking    int               `json:"checking"`
	Tracks      []LineTrackDTO    `json:"tracks"`
	Fallback    bool              `json:"fallback"`
	Stations    []LineStationDTO  `json:"stations"`
	Siding      []LineStationDTO  `json:"siding"`
	Mesh        LineMeshDTO       `json:"mesh"`
	// Git ordering (parent §3.4) is slice 6: reserved.
}

type LineKeyDTO struct {
	Kind    string `json:"kind"` // git | chart (Argo: repository and chart) | chart-name (Flux: by recorded name; repository not recorded)
	Repo    string `json:"repo"`
	RefKind string `json:"refKind,omitempty"`
	RefName string `json:"refName"`
	Label   string `json:"label"`
}

type LineKeyCountDTO struct {
	Key      LineKeyDTO `json:"key"`
	Stations int        `json:"stations"`
}

type LineTrackDTO struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Label string `json:"label"`
}

// LineProofDTO is how long a predicate has held, with how that is known:
// rule (a proven start), observed or sampled (began in (notBefore, since]),
// derived or unknown (start unknown; firstSeen). Capped: the proof was not
// kept (start unknown).
type LineProofDTO struct {
	Basis     string     `json:"basis"`
	Since     *time.Time `json:"since,omitempty"`
	NotBefore *time.Time `json:"notBefore,omitempty"`
	FirstSeen *time.Time `json:"firstSeen,omitempty"`
	AfterGap  bool       `json:"afterGap,omitempty"`
	Capped    bool       `json:"capped,omitempty"`
}

type LineFlagsDTO struct {
	Failing       bool   `json:"failing"`
	Compared      string `json:"compared,omitempty"` // "compared X, applied Y"
	ComparedVs    string `json:"comparedVs,omitempty"`
	Syncing       bool   `json:"syncing,omitempty"`
	Suspended     bool   `json:"suspended,omitempty"`
	SourceChanged bool   `json:"sourceChanged,omitempty"`
	ValuesDiffer  bool   `json:"valuesDiffer,omitempty"`
}

type LineUnitDTO struct {
	Origin        string `json:"origin"`
	Namespace     string `json:"namespace"`
	Name          string `json:"name"`
	SourceIndex   int    `json:"sourceIndex,omitempty"`
	UID           string `json:"uid,omitempty"`
	Applied       string `json:"applied,omitempty"` // empty: applied unknown
	Attempted     string `json:"attempted,omitempty"`
	AttemptPhase  string `json:"attemptPhase,omitempty"`
	Compared      string `json:"compared,omitempty"`
	Failing       bool   `json:"failing,omitempty"`
	Syncing       bool   `json:"syncing,omitempty"`
	Suspended     bool   `json:"suspended,omitempty"`
	SourceChanged bool   `json:"sourceChanged,omitempty"`
	// ConfiguredChartDiffers: "configured chart name differs from recorded
	// deployed chart" - current context, never a provenance claim.
	ConfiguredChartDiffers bool   `json:"configuredChartDiffers,omitempty"`
	Read                   string `json:"read"`
	// ContextRead: a recorded-name chart unit's current source read -
	// context only, disclosed apart, never gating its comparison.
	ContextRead string `json:"contextRead,omitempty"`
}

type LineExclusionDTO struct {
	Origin    string `json:"origin"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Reason    string `json:"reason"`
	Digest    string `json:"digest,omitempty"`
}

type LineStationDTO struct {
	Cluster      string             `json:"cluster"`
	Track        string             `json:"track"`
	Rank         int                `json:"rank"` // -1: no environment (the siding)
	Env          string             `json:"env"`
	State        string             `json:"state"`
	E            int                `json:"e"`
	A            []string           `json:"a"`
	U            int                `json:"u"`
	Single       string             `json:"single,omitempty"`
	Flags        LineFlagsDTO       `json:"flags"`
	Held         bool               `json:"held,omitempty"`
	HeldWhy      string             `json:"heldWhy,omitempty"`
	FailingSince *LineProofDTO      `json:"failingSince,omitempty"`
	HeldSince    *LineProofDTO      `json:"heldSince,omitempty"`
	Read         string             `json:"read"`
	ReadReasons  []string           `json:"readReasons,omitempty"` // "gitrepositories: stale"
	Incarnation  uint64             `json:"incarnation,omitempty"`
	ReadAt       *time.Time         `json:"readAt,omitempty"`
	AsOf         *time.Time         `json:"asOf,omitempty"` // the last good facts' time while not read ok
	Units        []LineUnitDTO      `json:"units"`          // the eligible units on the chosen key
	Excluded     []LineExclusionDTO `json:"excluded,omitempty"`
}

type LineMeshLinkDTO struct {
	A      string `json:"a"`
	B      string `json:"b"`
	Mutual bool   `json:"mutual"`
	From   string `json:"from,omitempty"`
}

// LineMeshDTO is the mesh on the board (parent §3.5) and its sample's
// freshness: "as of readAt", or the error when the last sample failed (the
// last good graph is kept).
type LineMeshDTO struct {
	Drawn    []LineMeshLinkDTO            `json:"drawn"`
	Card     map[string][]LineMeshLinkDTO `json:"card"`
	OffFleet []LineMeshLinkDTO            `json:"offFleet"`
	Unknown  []string                     `json:"unknown,omitempty"` // members whose peerings could not be read
	ReadAt   *time.Time                   `json:"readAt,omitempty"`
	Error    string                       `json:"error,omitempty"`
}

func keyDTO(k line.Key) LineKeyDTO {
	return LineKeyDTO{Kind: k.Kind, Repo: k.Repo, RefKind: k.RefKind, RefName: k.RefName, Label: k.String()}
}

func linksDTO(ls []line.MeshLink) []LineMeshLinkDTO {
	out := make([]LineMeshLinkDTO, 0, len(ls))
	for _, l := range ls {
		out = append(out, LineMeshLinkDTO{A: l.A, B: l.B, Mutual: l.Mutual, From: l.From})
	}
	return out
}

func revID(r *line.Revision) string {
	if r == nil {
		return ""
	}
	if r.SHA != "" {
		return r.SHA
	}
	return r.Version
}

// stationDTO maps one computed station; the service fills the proofs, read
// detail and times.
func stationDTO(sl line.StationLine, key *line.Key) LineStationDTO {
	d := LineStationDTO{Cluster: sl.Cluster, Track: sl.Track, Rank: sl.Rank, Env: sl.Env, State: sl.State,
		E: sl.E, A: append([]string{}, sl.A...), U: sl.U, Single: sl.Single, Held: sl.Held, HeldWhy: sl.HeldWhy, Read: sl.Read,
		Flags: LineFlagsDTO{Failing: sl.Flags.Failing, Compared: sl.Flags.Compared, ComparedVs: sl.Flags.ComparedVs,
			Syncing: sl.Flags.Syncing, Suspended: sl.Flags.Suspended, SourceChanged: sl.Flags.SourceChanged, ValuesDiffer: sl.Flags.ValuesDiffer},
		Units: []LineUnitDTO{}}
	if sl.Siding {
		d.Rank = line.RankNone
	}
	for _, u := range sl.Units {
		if key == nil || u.Key != *key {
			continue
		}
		d.Units = append(d.Units, LineUnitDTO{Origin: u.Origin, Namespace: u.Namespace, Name: u.Name, SourceIndex: u.SourceIndex, UID: u.UID,
			Applied: revID(u.Applied), Attempted: revID(u.Attempted), AttemptPhase: u.AttemptPhase, Compared: revID(u.Compared),
			Failing: u.Failing, Syncing: u.Syncing, Suspended: u.Suspended, SourceChanged: u.SourceChanged,
			ConfiguredChartDiffers: u.ConfiguredChartDiffers, Read: u.Read, ContextRead: u.ContextRead})
	}
	for _, x := range sl.Excluded {
		d.Excluded = append(d.Excluded, LineExclusionDTO{Origin: x.Origin, Namespace: x.Namespace, Name: x.Name, Reason: x.Reason, Digest: x.Digest})
	}
	return d
}

// lineDTO maps the computed line; stations are completed by the service.
func lineDTO(l line.Line) LineDTO {
	d := LineDTO{KeyHow: l.KeyHow, KeyStations: l.KeyStations, R: l.R, RStations: l.RStations, RTieBroken: l.RTieBroken, RTieBy: l.RTieBy,
		Applied: l.Applied, Compared: l.Compared, NotCompared: l.NotCompared, Checking: l.Checking,
		Fallback: l.Placement.Fallback, Keys: []LineKeyCountDTO{}, Tracks: []LineTrackDTO{}, Stations: []LineStationDTO{}, Siding: []LineStationDTO{}}
	if l.Key != nil {
		k := keyDTO(*l.Key)
		d.Key = &k
	}
	for _, kc := range l.Keys {
		d.Keys = append(d.Keys, LineKeyCountDTO{Key: keyDTO(kc.Key), Stations: kc.Stations})
	}
	for _, t := range l.Placement.Tracks {
		d.Tracks = append(d.Tracks, LineTrackDTO{Name: t.Name, Kind: t.Kind, Label: t.Label})
	}
	for _, sl := range l.Stations {
		d.Stations = append(d.Stations, stationDTO(sl, l.Key))
	}
	for _, sl := range l.Siding {
		d.Siding = append(d.Siding, stationDTO(sl, l.Key))
	}
	return d
}

func meshDTO(m line.MeshLines) LineMeshDTO {
	d := LineMeshDTO{Drawn: linksDTO(m.Drawn), Card: map[string][]LineMeshLinkDTO{}, OffFleet: linksDTO(m.OffFleet)}
	names := make([]string, 0, len(m.Card))
	for c := range m.Card {
		names = append(names, c)
	}
	sort.Strings(names)
	for _, c := range names {
		d.Card[c] = linksDTO(m.Card[c])
	}
	return d
}
