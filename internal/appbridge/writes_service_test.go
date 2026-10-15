package appbridge

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/lastwrite"
)

type writesConn struct {
	got []fleet.ObjectRef
	err error
}

func (w *writesConn) ObjectWrites(_ context.Context, refs []fleet.ObjectRef) ([]fleet.WriteResult, error) {
	w.got = refs
	if w.err != nil {
		return nil, w.err
	}
	when := time.Date(2026, 10, 15, 20, 31, 0, 0, time.UTC)
	return []fleet.WriteResult{
		{Ref: refs[0], State: fleet.WritesOK, Writes: lastwrite.Writes{
			Last:     &lastwrite.Record{Time: when.Add(time.Minute), Manager: "kube-controller-manager", Operation: "Update", Subresource: "status"},
			LastSpec: &lastwrite.Record{Time: when, Manager: "kustomize-controller", Operation: "Apply"}}},
		{Ref: refs[1], State: fleet.WritesError, Error: "forbidden"},
	}, nil
}

// The Line slice 7a: the bound read maps each result, keeps status updates
// labelled, and passes a failed read through as that object's error.
func TestObjectWritesServiceMapsResults(t *testing.T) {
	conn := &writesConn{}
	svc := NewObjectWritesService(func(c string) (ObjectWritesConn, bool) { return conn, c == "prd" })
	refs := []ObjectRefDTO{{Version: "v1", Kind: "ConfigMap", Namespace: "shop", Name: "settings", UID: "u1"},
		{Version: "v1", Kind: "Secret", Namespace: "shop", Name: "token", UID: "s1"}}
	d := svc.ObjectWrites("prd", refs)
	if d.Error != "" || len(d.Results) != 2 || conn.got[0].UID != "u1" {
		t.Fatalf("%+v", d)
	}
	r := d.Results[0]
	if r.State != "ok" || r.Last == nil || !r.Last.StatusUpdate || r.LastSpec == nil || r.LastSpec.StatusUpdate || r.LastSpec.Manager != "kustomize-controller" {
		t.Fatalf("result %+v last %+v spec %+v", r, r.Last, r.LastSpec)
	}
	if d.Results[1].State != "error" || d.Results[1].Error != "forbidden" || d.Results[1].Last != nil {
		t.Fatalf("a failed read %+v", d.Results[1])
	}
	if d := svc.ObjectWrites("gone", refs); d.Error == "" {
		t.Fatal("an unknown cluster was read")
	}
	conn.err = errors.New("at most 200 objects per read")
	if d := svc.ObjectWrites("prd", refs); d.Error == "" || len(d.Results) != 0 {
		t.Fatalf("a refused batch: %+v", d)
	}
}
