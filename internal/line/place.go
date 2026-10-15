package line

import (
	"sort"
	"strings"
)

// Station is one cluster from fleet.yaml, as the line places it.
type Station struct {
	Cluster string
	Env     string // environment, or tags.env
	Region  string // tags.region
	Group   string // group
	Order   int    // its position in fleet.yaml: the tie-break inside a cell
}

// Track kinds.
const (
	TrackRegion = "region"
	TrackGroup  = "group" // labelled "group: <name>", never as a region
	TrackFleet  = "fleet" // the single track when neither is set
)

// Track is one horizontal line of stations.
type Track struct {
	Name  string
	Kind  string
	Label string // "westeurope", "group: core", "Fleet"
}

// Placed is one station's position: its track, rank and order in its cell.
// A station with no recognised environment is on the siding.
type Placed struct {
	Station
	Track  string
	Rank   int
	Siding bool
}

// Placement is the board (§3.1).
type Placement struct {
	Tracks []Track
	Placed []Placed // on the line: track order, then rank, then fleet order
	Siding []Placed // no environment: off the line
	// Ranks is the distinct ranks present on the line, ascending; with fewer
	// than two, the board is one row with no comparison lighting.
	Ranks    []int
	Fallback bool
}

// Place puts every station on a track and a rank (§3.1). Tracks come from
// tags.region; with no region anywhere, from group (labelled as a group);
// otherwise one "Fleet" track. A station missing the chosen field joins a
// track named for the absence.
func Place(stations []Station) Placement {
	var pl Placement
	byRegion := anyField(stations, func(s Station) string { return s.Region })
	byGroup := !byRegion && anyField(stations, func(s Station) string { return s.Group })
	trackOf := func(s Station) (string, string, string) {
		switch {
		case byRegion:
			n := strings.TrimSpace(s.Region)
			if n == "" {
				return "", TrackRegion, "no region"
			}
			return n, TrackRegion, n
		case byGroup:
			n := strings.TrimSpace(s.Group)
			if n == "" {
				return "", TrackGroup, "no group"
			}
			return n, TrackGroup, "group: " + n
		default:
			return "fleet", TrackFleet, "Fleet"
		}
	}
	seen := map[string]bool{}
	ranks := map[int]bool{}
	for _, s := range stations {
		name, kind, label := trackOf(s)
		p := Placed{Station: s, Track: name, Rank: EnvRank(s.Env)}
		if p.Rank == RankNone {
			p.Siding = true
			pl.Siding = append(pl.Siding, p)
			continue
		}
		if !seen[name] {
			seen[name] = true
			pl.Tracks = append(pl.Tracks, Track{Name: name, Kind: kind, Label: label})
		}
		ranks[p.Rank] = true
		pl.Placed = append(pl.Placed, p)
	}
	sort.SliceStable(pl.Tracks, func(i, j int) bool {
		a, b := pl.Tracks[i].Name, pl.Tracks[j].Name
		if (a == "") != (b == "") {
			return b == "" // the absence track last
		}
		return a < b
	})
	trackIdx := map[string]int{}
	for i, t := range pl.Tracks {
		trackIdx[t.Name] = i
	}
	sort.SliceStable(pl.Placed, func(i, j int) bool {
		a, b := pl.Placed[i], pl.Placed[j]
		if trackIdx[a.Track] != trackIdx[b.Track] {
			return trackIdx[a.Track] < trackIdx[b.Track]
		}
		if a.Rank != b.Rank {
			return a.Rank < b.Rank
		}
		return a.Order < b.Order
	})
	sort.SliceStable(pl.Siding, func(i, j int) bool { return pl.Siding[i].Order < pl.Siding[j].Order })
	for r := range ranks {
		pl.Ranks = append(pl.Ranks, r)
	}
	sort.Ints(pl.Ranks)
	pl.Fallback = len(pl.Ranks) < 2
	return pl
}

func anyField(stations []Station, f func(Station) string) bool {
	for _, s := range stations {
		if strings.TrimSpace(f(s)) != "" {
			return true
		}
	}
	return false
}
