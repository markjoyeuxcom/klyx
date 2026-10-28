package hubble

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/metrics"
	"github.com/moomora/klyx/internal/safehttp"
)

// Coverage C5 §5: the metrics client returns labels as sent, so a label
// value Klyx shows - a drop reason, its protocol - loses the credential its
// endpoint echoed where it is shown.
func TestADropReasonEchoingTheCredentialIsShownWithoutIt(t *testing.T) {
	token := "C5-HUBBLE-TOKEN"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, vec(el(`"reason":"denied for `+token+`","protocol":"`+token+`"`, "3")))
	}))
	defer srv.Close()
	q := metrics.NewDirectTransport(srv.URL, token, safehttp.NewClient(metrics.DirectPolicy(3*time.Second, false)))
	stats, st, err := NewSource(metrics.NewClient(q, metrics.Scope{})).QueryFlowStats(context.Background())
	if err != nil || !st.Available || len(stats.DropReasons) != 1 {
		t.Fatalf("read: %+v %+v %v", stats, st, err)
	}
	d := stats.DropReasons[0]
	if strings.Contains(d.Reason+d.Protocol, token) || d.Reason != "denied for "+safehttp.Redacted || d.Rate != 3 {
		t.Fatalf("drop reason: %+v", d)
	}
}
