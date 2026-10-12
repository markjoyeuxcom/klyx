package appbridge

import (
	"encoding/json"
	"hash/maphash"
	"reflect"
	"sort"
	"sync"

	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/observe"
)

// Rows deltas (1c slice 3 addendum §3.3). The verdict parts with large row
// sets publish a full set once, then only what changed, against one
// lineage per (cluster, incarnation, task, part): the last state the sink
// published. Bootstrap replies come from the same lineage, so a subscriber
// never holds a Ver the broadcast did not publish - the next delta's base
// is always one it can hold.

// deltaParts are the parts that travel as deltas. Their lenses sort rows
// themselves, so row order is not part of the contract; every other part
// keeps full pushes.
var deltaParts = map[string]bool{
	healthsrc.TaskPods + "/pods":           true,
	healthsrc.TaskWorkloads + "/workloads": true,
	healthsrc.TaskNodes + "/nodes":         true,
}

// uidRow is a delta part's row: its object UID keys upserts and removals.
type uidRow interface{ rowUID() string }

type lineageKey struct {
	cluster     string
	incarnation uint64
	task, part  string
}

func lineageKeyOf(dto RowsDTO) lineageKey {
	return lineageKey{cluster: dto.Cluster, incarnation: dto.Incarnation, task: dto.Task, part: dto.Part.ID}
}

// lineageState is a key's last published state. fp is nil when its rows
// lack valid unique UIDs: the next publication is full.
type lineageState struct {
	ver  uint64
	full RowsDTO
	fp   map[string]uint64
}

// rowsLineage is shared by the sink (publications) and the service
// (bootstraps and subscription lifetimes). A key has a state only while a
// subscription retains it; a newer incarnation of the cluster and its
// Final drop older states.
type rowsLineage struct {
	mu     sync.Mutex
	seed   maphash.Seed
	refs   map[lineageKey]int
	states map[lineageKey]*lineageState
	newest map[string]uint64 // per cluster: the newest incarnation seen
	final  map[string]uint64 // per cluster: finalized through
}

func newRowsLineage() *rowsLineage {
	return &rowsLineage{seed: maphash.MakeSeed(), refs: map[lineageKey]int{}, states: map[lineageKey]*lineageState{},
		newest: map[string]uint64{}, final: map[string]uint64{}}
}

func (l *rowsLineage) retain(k lineageKey) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.refs[k]++
}

// release drops a key's state with its last subscription.
func (l *rowsLineage) release(k lineageKey) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.refs[k]--; l.refs[k] <= 0 {
		delete(l.refs, k)
		delete(l.states, k)
	}
}

// summary drops the states a newer incarnation or a Final supersedes.
func (l *rowsLineage) summary(s observe.Summary) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.noteLocked(s.Cluster, s.Incarnation)
	if s.Final && s.Incarnation > l.final[s.Cluster] {
		l.final[s.Cluster] = s.Incarnation
		for k := range l.states {
			if k.cluster == s.Cluster && k.incarnation <= s.Incarnation {
				delete(l.states, k)
			}
		}
	}
}

func (l *rowsLineage) noteLocked(cluster string, incarnation uint64) {
	if incarnation <= l.newest[cluster] {
		return
	}
	l.newest[cluster] = incarnation
	for k := range l.states {
		if k.cluster == cluster && k.incarnation < incarnation {
			delete(l.states, k)
		}
	}
}

// tracked: the key keeps a lineage - a delta part, retained by a
// subscription, of the cluster's newest incarnation and not finalized.
func (l *rowsLineage) trackedLocked(k lineageKey) bool {
	return deltaParts[k.task+"/"+k.part] && l.refs[k] > 0 &&
		k.incarnation >= l.newest[k.cluster] && k.incarnation > l.final[k.cluster]
}

// publish turns one converted publication into the message to emit: full
// without a state (or without valid unique UIDs), else a delta against the
// state. emit is false for a Ver the lineage already holds - every
// subscriber holds it or reads it from the lineage.
func (l *rowsLineage) publish(dto RowsDTO) (msg RowsDTO, emit bool) {
	dto.Full = true
	k := lineageKeyOf(dto)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.noteLocked(k.cluster, k.incarnation)
	if !l.trackedLocked(k) {
		return dto, true
	}
	prev := l.states[k]
	if prev != nil && dto.Part.Ver <= prev.ver {
		return RowsDTO{}, false
	}
	uids, fp := l.prints(dto.Rows)
	l.states[k] = &lineageState{ver: dto.Part.Ver, full: dto, fp: fp}
	if prev == nil || prev.fp == nil || fp == nil {
		return dto, true
	}
	rows := reflect.ValueOf(dto.Rows)
	ups := reflect.MakeSlice(rows.Type(), 0, 0)
	for i, uid := range uids {
		if old, ok := prev.fp[uid]; !ok || old != fp[uid] {
			ups = reflect.Append(ups, rows.Index(i))
		}
	}
	removed := []string{}
	for uid := range prev.fp {
		if _, ok := fp[uid]; !ok {
			removed = append(removed, uid)
		}
	}
	sort.Strings(removed)
	msg = dto
	msg.Full, msg.Base, msg.Rows, msg.Upserts, msg.Removed = false, prev.ver, nil, ups.Interface(), removed
	return msg, true
}

// current is the key's published state, in full: a bootstrap's reply.
func (l *rowsLineage) current(k lineageKey) (RowsDTO, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if st := l.states[k]; st != nil && l.trackedLocked(k) {
		return st.full, true
	}
	return RowsDTO{}, false
}

// seedFrom answers a bootstrap that found no state with its own capture,
// which becomes the published state. A broadcast that published first wins:
// its state is the answer, never a Ver beside the lineage.
func (l *rowsLineage) seedFrom(dto RowsDTO) RowsDTO {
	dto.Full = true
	k := lineageKeyOf(dto)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.noteLocked(k.cluster, k.incarnation)
	if !l.trackedLocked(k) {
		return dto
	}
	if st := l.states[k]; st != nil {
		return st.full
	}
	_, fp := l.prints(dto.Rows)
	l.states[k] = &lineageState{ver: dto.Part.Ver, full: dto, fp: fp}
	return dto
}

// prints hashes each row's sent JSON by UID, in row order. fp is nil when a
// row has no UID or two rows share one: such a set travels only in full.
// What is hashed is exactly what is sent - nothing is excluded.
func (l *rowsLineage) prints(rows any) (uids []string, fp map[string]uint64) {
	v := reflect.ValueOf(rows)
	if v.Kind() != reflect.Slice {
		return nil, nil
	}
	uids = make([]string, v.Len())
	fp = make(map[string]uint64, v.Len())
	for i := range v.Len() {
		r, ok := v.Index(i).Interface().(uidRow)
		if !ok {
			return nil, nil
		}
		uid := r.rowUID()
		if _, dup := fp[uid]; uid == "" || dup {
			return nil, nil
		}
		b, err := json.Marshal(r)
		if err != nil {
			return nil, nil
		}
		uids[i], fp[uid] = uid, maphash.Bytes(l.seed, b)
	}
	return uids, fp
}
