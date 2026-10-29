package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"time"

	"github.com/moomora/klyx/internal/endpointhttp"
)

// Settings S3a §6: Detect, in one cluster, proposes and never proves. It
// runs the existing discovery, with its proof of one installation, and
// reads one bounded sample of the installation's alerts. Receiving and
// parsing that response is reading alert bodies, which the person asked
// for; the parser keeps label names, and the values of labels equal to a
// fleet cluster's name, and no decoded alert. Annotations and every other
// value are never kept.

const (
	detectMaxBytes  = 1 << 20
	detectMaxAlerts = 500
)

var errDetectTooLarge = errors.New("the sample was larger than 1 MiB")

// DetectSample is what Detect read in one cluster.
type DetectSample struct {
	Cluster string
	// the installation discovery proved there: Found, with its Service
	// and how it was chosen; or none, or ambiguous, or unreadable (Err)
	Found     bool
	None      bool
	Ambiguous string
	Err       string
	Namespace string
	Service   string
	Port      string
	Path      string
	Via       string
	// the sample: when it was read, the alerts in it, whether it was cut
	// at its bounds, and why it could not be read
	At        time.Time
	Alerts    int
	Truncated bool
	Unread    string
	// LabelNames are the label names seen; Members, for each label, the
	// fleet clusters its values named
	LabelNames []string
	Members    map[string][]string
}

// DetectAlertmanager runs Detect in cluster (S3a §6): its discovery, then,
// for a proven installation, one sample through the cluster's own Service
// proxy, in a fleet-wide slot, under a lease on the cluster.
func (r *Registry) DetectAlertmanager(ctx context.Context, cluster string, fleetNames []string) (out DetectSample) {
	out = DetectSample{Cluster: cluster}
	r.mu.RLock()
	b := r.alerts
	r.mu.RUnlock()
	if b == nil {
		out.Err = "alert installations are not available"
		return out
	}
	conn, lctx, release, err := b.lease(cluster, "Detect's sample in "+cluster)
	if err != nil {
		out.Err = fmt.Sprintf("%s is not connected: %v", cluster, err)
		return out
	}
	defer release()
	c, ok := conn.(*ClusterConn)
	if !ok {
		out.Err = cluster + " cannot be read for Detect"
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	stop := context.AfterFunc(lctx, cancel) // the cluster's retirement ends it
	defer stop()
	// what it read settles only while its lease and its deadline hold: an
	// ended attempt's discovery and sample are discarded, never proposed,
	// however far its reads got (Codex's review of S3a-4b, R1). It runs
	// before the lease is released.
	defer func() {
		switch {
		case lctx.Err() != nil:
			out = DetectSample{Cluster: cluster, Err: cluster + " was reconnected or removed while Detect read it, so what it read is discarded"}
		case ctx.Err() != nil:
			out = DetectSample{Cluster: cluster, Err: fmt.Sprintf("Detect in %s did not finish in time: %v", cluster, ctx.Err())}
		}
	}()
	select {
	case amReads <- struct{}{}:
		defer func() { <-amReads }()
	case <-ctx.Done():
		out.Err = fmt.Sprintf("Detect in %s did not run in time: %v", cluster, ctx.Err())
		return out
	}
	d := c.discoverAlertmanager(ctx)
	switch {
	case d.err != nil:
		out.Err = fmt.Sprintf("discovery in %s could not be read: %v", cluster, d.err)
		return out
	case d.ambiguous != "":
		out.Ambiguous = d.ambiguous
		return out
	case d.none:
		out.None = true
		return out
	}
	out.Found, out.Namespace, out.Service, out.Port, out.Path, out.Via = true, d.svc.Namespace, d.svc.Name, d.svc.Port, d.svc.Path, d.via
	rc, err := c.amProxyREST()
	if err != nil {
		out.Unread = "the service-proxy client could not be built: " + err.Error()
		return out
	}
	p := endpointhttp.NewProxy(rc, d.svc, "", endpointhttp.Limits{MaxBytes: detectMaxBytes, TooLarge: errDetectTooLarge})
	out.At = b.clk.Now()
	code, body, err := p.Get(ctx, "/api/v2/alerts", url.Values{"active": {"true"}, "silenced": {"true"}, "inhibited": {"true"}})
	switch {
	case errors.Is(err, errDetectTooLarge):
		out.Unread = "the sample was larger than 1 MiB, so it was not read"
		return out
	case err != nil:
		out.Unread = "the sample could not be read: " + err.Error()
		return out
	case code != http.StatusOK:
		out.Unread = fmt.Sprintf("the sample could not be read: Alertmanager answered HTTP %d", code)
		return out
	}
	names, members, n, cut, err := sampleLabels(body, fleetNames)
	if err != nil {
		out.Unread = "the sample could not be read: " + err.Error()
		return out
	}
	out.LabelNames, out.Members, out.Alerts, out.Truncated = names, members, n, cut
	return out
}

// sampleLabels reads at most detectMaxAlerts alerts of a sample, keeping
// each label's name, and its value only when it equals a fleet cluster's
// name. An alert's annotations are never decoded.
func sampleLabels(body []byte, fleetNames []string) (names []string, members map[string][]string, n int, cut bool, err error) {
	fleet := map[string]bool{}
	for _, f := range fleetNames {
		fleet[f] = true
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	tok, err := dec.Token()
	if err != nil {
		return nil, nil, 0, false, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return nil, nil, 0, false, errors.New("the sample is not a list of alerts")
	}
	seen := map[string]bool{}
	byLabel := map[string]map[string]bool{}
	for dec.More() {
		if n == detectMaxAlerts {
			return sorted(seen), membersOf(byLabel), n, true, nil
		}
		var a struct {
			Labels map[string]string `json:"labels"`
		}
		if err := dec.Decode(&a); err != nil {
			return nil, nil, 0, false, err
		}
		n++
		for k, v := range a.Labels {
			seen[k] = true
			if fleet[v] {
				if byLabel[k] == nil {
					byLabel[k] = map[string]bool{}
				}
				byLabel[k][v] = true
			}
		}
	}
	// a complete sample is one list and nothing after it (R3)
	if t, err := dec.Token(); err != nil || t != json.Delim(']') {
		return nil, nil, 0, false, errors.New("the sample is not a complete list of alerts")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, nil, 0, false, errors.New("the sample has more after its list of alerts")
	}
	return sorted(seen), membersOf(byLabel), n, false, nil
}

func sorted(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func membersOf(byLabel map[string]map[string]bool) map[string][]string {
	out := map[string][]string{}
	for k, vs := range byLabel {
		out[k] = sorted(vs)
	}
	return out
}
