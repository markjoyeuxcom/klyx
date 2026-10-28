package metrics

import (
	"context"
	"testing"
)

// fakeQuerier returns canned status/body and records the last query.
type fakeQuerier struct {
	status int
	body   string
	err    error
	lastQ  string
}

func (f *fakeQuerier) InstantQuery(_ context.Context, q string) (int, []byte, error) {
	f.lastQ = q
	return f.status, []byte(f.body), f.err
}

func TestInstantScalar(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantValue  float64
		wantAbsent bool
		wantErr    bool
	}{
		{name: "scalar", status: 200, body: `{"status":"success","data":{"resultType":"scalar","result":[1700000000,"0.42"]}}`, wantValue: 0.42},
		{name: "single vector", status: 200, body: `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1700000000,"0.61"]}]}}`, wantValue: 0.61},
		{name: "empty vector is absent", status: 200, body: `{"status":"success","data":{"resultType":"vector","result":[]}}`, wantAbsent: true},
		{name: "status error", status: 200, body: `{"status":"error","error":"bad query"}`, wantErr: true},
		{name: "non-prometheus body", status: 200, body: `<html>grafana</html>`, wantErr: true},
		{name: "multi-element vector", status: 200, body: `{"status":"success","data":{"resultType":"vector","result":[{"value":[1,"1"]},{"value":[1,"2"]}]}}`, wantErr: true},
		{name: "http 503", status: 503, body: `service unavailable`, wantErr: true},
		{name: "NaN scalar is absent", status: 200, body: `{"status":"success","data":{"resultType":"scalar","result":[1700000000,"NaN"]}}`, wantAbsent: true},
		{name: "Inf vector is absent", status: 200, body: `{"status":"success","data":{"resultType":"vector","result":[{"value":[1700000000,"+Inf"]}]}}`, wantAbsent: true},
		{name: "scalar wrong arity errors", status: 200, body: `{"status":"success","data":{"resultType":"scalar","result":[1,"2",3]}}`, wantErr: true},
		{name: "numeric value errors", status: 200, body: `{"status":"success","data":{"resultType":"scalar","result":[1,1]}}`, wantErr: true},
		{name: "null body rejected", status: 200, body: `null`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewClient(&fakeQuerier{status: tc.status, body: tc.body}, Scope{})
			s, err := c.InstantScalar(context.Background(), raw("q"))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %+v", s)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if s.Absent != tc.wantAbsent {
				t.Fatalf("absent: want %v got %v", tc.wantAbsent, s.Absent)
			}
			if !tc.wantAbsent && s.Value != tc.wantValue {
				t.Fatalf("value: want %v got %v", tc.wantValue, s.Value)
			}
		})
	}
}

func TestLiveness(t *testing.T) {
	ok := NewClient(&fakeQuerier{status: 200, body: `{"status":"success","data":{"resultType":"vector","result":[{"value":[1,"1"]}]}}`}, Scope{})
	if err := ok.Liveness(context.Background()); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
	bad := NewClient(&fakeQuerier{status: 401, body: `unauthorized`}, Scope{})
	if err := bad.Liveness(context.Background()); err == nil {
		t.Fatal("want error on 401")
	}
}
