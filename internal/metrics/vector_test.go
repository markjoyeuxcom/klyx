package metrics

import (
	"context"
	"testing"
)

func TestInstantVector(t *testing.T) {
	body := `{"status":"success","data":{"resultType":"vector","result":[
		{"metric":{"envoy_cluster_name":"a"},"value":[1,"1.5"]},
		{"metric":{"envoy_cluster_name":"b"},"value":[1,"NaN"]},
		{"metric":{"envoy_cluster_name":"d"},"value":[1,"+Inf"]},
		{"metric":{"envoy_cluster_name":"c"},"value":[1,"3"]}
	]}}`
	c := NewClient(TransportOf(&fakeQuerier{status: 200, body: body}), Scope{})
	out, err := c.InstantVector(context.Background(), raw("q"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// NaN ("b") and +Inf ("d") are both filtered out.
	if len(out) != 2 {
		t.Fatalf("want 2 samples (NaN and +Inf filtered), got %d: %+v", len(out), out)
	}
	if out[0].Labels["envoy_cluster_name"] != "a" || out[0].Value != 1.5 {
		t.Fatalf("sample 0 wrong: %+v", out[0])
	}
	if out[1].Labels["envoy_cluster_name"] != "c" || out[1].Value != 3 {
		t.Fatalf("sample 1 wrong: %+v", out[1])
	}
}

func TestInstantVectorEmptyAndErrors(t *testing.T) {
	empty := NewClient(TransportOf(&fakeQuerier{status: 200, body: `{"status":"success","data":{"resultType":"vector","result":[]}}`}), Scope{})
	out, err := empty.InstantVector(context.Background(), raw("q"))
	if err != nil || len(out) != 0 {
		t.Fatalf("empty vector: want 0/nil, got %d/%v", len(out), err)
	}
	notvec := NewClient(TransportOf(&fakeQuerier{status: 200, body: `{"status":"success","data":{"resultType":"scalar","result":[1,"1"]}}`}), Scope{})
	if _, err := notvec.InstantVector(context.Background(), raw("q")); err == nil {
		t.Fatal("want error on non-vector result")
	}
	bad := NewClient(TransportOf(&fakeQuerier{status: 503, body: "down"}), Scope{})
	if _, err := bad.InstantVector(context.Background(), raw("q")); err == nil {
		t.Fatal("want error on HTTP 503")
	}
}
