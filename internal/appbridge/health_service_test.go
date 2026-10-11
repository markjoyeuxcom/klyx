package appbridge

import (
	"testing"

	"github.com/moomora/klyx/internal/observe"
)

type recEmitter struct{ names []string }

func (r *recEmitter) Emit(name string, _ any) { r.names = append(r.names, name) }

func TestHealthSinkEventNames(t *testing.T) {
	em := &recEmitter{}
	s := NewHealthSink(em)
	s.Summary(observe.Summary{Cluster: "dev"})
	s.Rows(observe.Rows{Cluster: "dev", Task: "pods", Part: observe.Part{ID: "pods"}})
	if len(em.names) != 2 || em.names[0] != "health:summary" || em.names[1] != "health:rows:dev:pods/pods" {
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
