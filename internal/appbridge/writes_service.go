package appbridge

import (
	"context"
	"time"

	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/lastwrite"
)

// ObjectWritesConn is what the managed-field read needs of a cluster.
type ObjectWritesConn interface {
	ObjectWrites(ctx context.Context, refs []fleet.ObjectRef) ([]fleet.WriteResult, error)
}

// ObjectWritesService is The Line slice 7a, bound to JS: each object's last
// recorded managed-field update (parent §8.3) - when, by which manager,
// with which operation and subresource. It is not an audit log: a manager
// is a workflow identifier, not an authenticated writer, and nothing says
// which field changed. Slice 8's dependency graph reads it for its nodes
// and their supply objects.
type ObjectWritesService struct {
	lookup func(string) (ObjectWritesConn, bool)
}

func NewObjectWritesService(lookup func(string) (ObjectWritesConn, bool)) *ObjectWritesService {
	return &ObjectWritesService{lookup: lookup}
}

// ObjectRefDTO names one object, pinned to its UID.
type ObjectRefDTO struct {
	Group     string `json:"group"`
	Version   string `json:"version"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	UID       string `json:"uid,omitempty"`
}

// ManagedWriteDTO is one managed-field entry.
type ManagedWriteDTO struct {
	Time         time.Time `json:"time"`
	Manager      string    `json:"manager"`
	Operation    string    `json:"operation"`
	Subresource  string    `json:"subresource,omitempty"`
	StatusUpdate bool      `json:"statusUpdate"` // labelled "status update"; never change proximity
}

// ObjectWriteDTO is one object's read: state ok, "no record" (absent or
// reset managedFields), replaced (another UID now), gone, or error.
type ObjectWriteDTO struct {
	Ref      ObjectRefDTO     `json:"ref"`
	State    string           `json:"state"`
	Last     *ManagedWriteDTO `json:"last,omitempty"`
	LastSpec *ManagedWriteDTO `json:"lastSpec,omitempty"` // the latest that is not a status update
	Error    string           `json:"error,omitempty"`
}

type ObjectWritesDTO struct {
	Results []ObjectWriteDTO `json:"results"`
	Error   string           `json:"error,omitempty"`
}

const writesTimeout = 15 * time.Second

// ObjectWrites reads up to 200 objects' last recorded managed-field updates.
func (s *ObjectWritesService) ObjectWrites(cluster string, refs []ObjectRefDTO) ObjectWritesDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return ObjectWritesDTO{Results: []ObjectWriteDTO{}, Error: "cluster not connected: " + cluster}
	}
	in := make([]fleet.ObjectRef, 0, len(refs))
	for _, r := range refs {
		in = append(in, fleet.ObjectRef{Group: r.Group, Version: r.Version, Kind: r.Kind, Namespace: r.Namespace, Name: r.Name, UID: r.UID})
	}
	ctx, cancel := context.WithTimeout(context.Background(), writesTimeout)
	defer cancel()
	res, err := conn.ObjectWrites(ctx, in)
	if err != nil {
		return ObjectWritesDTO{Results: []ObjectWriteDTO{}, Error: err.Error()}
	}
	out := ObjectWritesDTO{Results: make([]ObjectWriteDTO, 0, len(res))}
	for i, r := range res {
		out.Results = append(out.Results, ObjectWriteDTO{Ref: refs[i], State: r.State, Last: writeDTO(r.Writes.Last),
			LastSpec: writeDTO(r.Writes.LastSpec), Error: r.Error})
	}
	return out
}

func writeDTO(r *lastwrite.Record) *ManagedWriteDTO {
	if r == nil {
		return nil
	}
	return &ManagedWriteDTO{Time: r.Time, Manager: r.Manager, Operation: r.Operation, Subresource: r.Subresource, StatusUpdate: r.StatusUpdate()}
}
