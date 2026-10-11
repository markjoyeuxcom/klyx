package appbridge

import (
	"testing"

	"github.com/moomora/klyx/internal/certmanager"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/observe"
)

type recEmitter struct{ names []string }

func (r *recEmitter) Emit(name string, _ any) { r.names = append(r.names, name) }

func TestHealthSinkEventNames(t *testing.T) {
	em := &recEmitter{}
	s := NewHealthSink(em)
	s.Summary(observe.Summary{Cluster: "dev"})
	// A part no lens reads yet (no converter) is not emitted; a converted
	// part goes out on its task/part name (slice 3 §2).
	s.Rows(observe.Rows{Cluster: "dev", Task: "helm", Part: observe.Part{ID: "releases"}})
	s.Rows(observe.Rows{Cluster: "dev", Task: healthsrc.TaskCerts, Part: observe.Part{ID: "certificates"},
		Rows: healthsrc.RowSet[certmanager.Certificate]{}})
	if len(em.names) != 2 || em.names[0] != "health:summary" || em.names[1] != "health:rows:dev:certs/certificates" {
		t.Fatalf("events: %v", em.names)
	}
}

func TestSummariesSkipsObserversWithoutAPublication(t *testing.T) {
	o := observe.New(observe.Config{Cluster: "dev"})
	svc := NewHealthService(func() []*observe.Observer { return []*observe.Observer{o} })
	if got := svc.Summaries(); len(got) != 0 {
		t.Fatalf("an Observer that has not published contributes nothing: %+v", got)
	}
}
