package metrics

import (
	"context"
	"testing"
)

func TestProbe(t *testing.T) {
	t.Run("unavailable resolution stays unavailable", func(t *testing.T) {
		cap := Probe(context.Background(), Resolution{Mode: ModeUnavailable, Reason: "no Prometheus Service found"})
		if cap.Available || cap.Reason == "" {
			t.Fatalf("got %+v", cap)
		}
	})
	t.Run("live probe passes", func(t *testing.T) {
		q := &fakeQuerier{status: 200, body: `{"status":"success","data":{"resultType":"vector","result":[{"value":[1,"1"]}]}}`}
		cap := Probe(context.Background(), Resolution{Mode: ModeDiscovered, Source: "monitoring/p:9090", Transport: TransportOf(q)})
		if !cap.Available || cap.Mode != ModeDiscovered || cap.Source != "monitoring/p:9090" {
			t.Fatalf("got %+v", cap)
		}
	})
	t.Run("probe failure carries the real reason", func(t *testing.T) {
		q := &fakeQuerier{status: 401, body: `unauthorized`}
		cap := Probe(context.Background(), Resolution{Mode: ModeExplicitEndpoint, Source: "https://host", Transport: TransportOf(q)})
		if cap.Available || cap.Reason == "" {
			t.Fatalf("got %+v", cap)
		}
	})
}
