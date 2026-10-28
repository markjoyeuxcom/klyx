package metrics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"k8s.io/client-go/rest"

	"github.com/moomora/klyx/internal/endpointhttp"
)

// Settings S3b §6: Detect's one bounded metadata sample of a Service it
// found in a member - never series and never sample values. One labels
// answer for a requested window, then the values of at most eight labels
// in a fixed order, each answer capped at 1 MiB and examined in its first
// 2,000 values, counted before any filtering: a value past them is never
// examined. Only label names, and values exactly equal to a fleet
// cluster's name, are kept; never another value, an error body or a
// number. Its paths are this package's own, as the query templates' are
// (S3b §3.3), and a label name enters one only when it is a plain label
// name, which needs no escaping.

const (
	metadataMaxBytes  = 1 << 20
	metadataMaxValues = 2000
	metadataMaxLabels = 8
	metadataWindow    = time.Hour
)

var (
	errMetadataTooLarge = errors.New("the answer was larger than 1 MiB")
	errMetadataShape    = errors.New("the answer is not a successful list of values")
)

// memberLabelsFirst are the labels whose values a sample reads first, in
// this order; then the others, alphabetically.
var memberLabelsFirst = []string{"cluster", "k8s_cluster_name", "cluster_name", "kubernetes_cluster"}

// plainLabel is the label-name grammar a path takes.
var plainLabel = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// MetadataSample is one metadata sample: what it asked for, the label
// names seen and, for each label whose values it read, the fleet clusters
// among them.
type MetadataSample struct {
	// Start and End are the window asked for: Prometheus's metadata bounds
	// are approximate, so it is not the window the answer covers
	Start, End time.Time
	// Names are the label names among the labels answer's first 2,000;
	// NamesCut, that the answer held more or was larger than 1 MiB
	Names    []string
	NamesCut bool
	// Members are, for each label whose values were read, the fleet
	// clusters among its first 2,000 values
	Members map[string][]string
	// Queried are the labels whose values were read, in order; Cut, those
	// whose answer held more or was larger than 1 MiB; Failed, those whose
	// values could not be read; NotQueried counts the names past the eight
	// or not plain, whose values were not asked for
	Queried, Cut, Failed []string
	NotQueried           int
}

// SampleMetadata reads one sample from c through the cluster's guarded
// proxy client r, each request under fence. An error is the labels
// answer's: with no names, nothing is said of the members.
func SampleMetadata(ctx context.Context, r rest.Interface, c ServiceCandidate, fence Fence, fleetNames []string, now time.Time) (MetadataSample, error) {
	svc := endpointhttp.Service{Namespace: c.Namespace, Name: c.Name, Port: c.Port, Scheme: schemeOr(c.Scheme), Path: c.Path}
	p := endpointhttp.NewProxy(r, svc, "", endpointhttp.Limits{MaxBytes: metadataMaxBytes, TooLarge: errMetadataTooLarge})
	out := MetadataSample{Start: now.Add(-metadataWindow), End: now, Members: map[string][]string{}}
	window := url.Values{"start": {strconv.FormatInt(out.Start.Unix(), 10)}, "end": {strconv.FormatInt(out.End.Unix(), 10)}}
	read := func(path string) ([]string, bool, error) {
		code, body, err := fence.Do(ctx, func(ctx context.Context) (int, []byte, error) { return p.Get(ctx, path, window) })
		switch {
		case errors.Is(err, errMetadataTooLarge):
			return nil, true, nil
		case err != nil:
			return nil, false, err
		case code != http.StatusOK:
			return nil, false, fmt.Errorf("Prometheus answered HTTP %d", code)
		}
		return firstValues(body)
	}
	names, cut, err := read("/api/v1/labels")
	if err != nil {
		return out, fmt.Errorf("the label names could not be read: %w", err)
	}
	out.NamesCut = cut
	seen := map[string]bool{}
	for _, n := range names {
		seen[n] = true
	}
	out.Names = sortedKeys(seen)
	fleet := map[string]bool{}
	for _, f := range fleetNames {
		fleet[f] = true
	}
	for _, l := range sampleOrder(seen) {
		if !plainLabel.MatchString(l) || len(out.Queried) == metadataMaxLabels || ctx.Err() != nil {
			out.NotQueried++
			continue
		}
		out.Queried = append(out.Queried, l)
		vals, cut, err := read("/api/v1/label/" + l + "/values")
		if err != nil {
			out.Failed = append(out.Failed, l)
			continue
		}
		if cut {
			out.Cut = append(out.Cut, l)
		}
		members := map[string]bool{}
		for _, v := range vals {
			if fleet[v] {
				members[v] = true
			}
		}
		if len(members) > 0 {
			out.Members[l] = sortedKeys(members)
		}
	}
	return out, nil
}

// sampleOrder is the order labels' values are read in: the member labels
// first, whether or not the names answer listed them (it may have been
// cut), then the other names alphabetically; a reserved name (__name__) is
// never a member label, and is not asked for.
func sampleOrder(names map[string]bool) []string {
	out := append([]string(nil), memberLabelsFirst...)
	first := map[string]bool{}
	for _, l := range memberLabelsFirst {
		first[l] = true
	}
	for _, n := range sortedKeys(names) {
		if !first[n] && !strings.HasPrefix(n, "__") {
			out = append(out, n)
		}
	}
	return out
}

// firstValues are the first 2,000 values of a successful answer's list, and
// whether it held more. Every other field - a warning, an error - is
// passed over, never kept.
func firstValues(body []byte) (values []string, cut bool, err error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, false, errMetadataShape
	}
	status, data := "", false
	for dec.More() {
		t, err := dec.Token()
		key, ok := t.(string)
		if err != nil || !ok {
			return nil, false, errMetadataShape
		}
		switch key {
		case "status":
			if err := dec.Decode(&status); err != nil {
				return nil, false, errMetadataShape
			}
		case "data":
			if data {
				return nil, false, errMetadataShape
			}
			data = true
			if t, err := dec.Token(); err != nil || t != json.Delim('[') {
				return nil, false, errMetadataShape
			}
			for dec.More() {
				if len(values) == metadataMaxValues {
					// counted before any filtering: never examined
					cut = true
					var past json.RawMessage
					if err := dec.Decode(&past); err != nil {
						return nil, false, errMetadataShape
					}
					continue
				}
				var v string
				if err := dec.Decode(&v); err != nil {
					return nil, false, errMetadataShape
				}
				values = append(values, v)
			}
			if t, err := dec.Token(); err != nil || t != json.Delim(']') {
				return nil, false, errMetadataShape
			}
		default:
			var other json.RawMessage
			if err := dec.Decode(&other); err != nil {
				return nil, false, errMetadataShape
			}
		}
	}
	if t, err := dec.Token(); err != nil || t != json.Delim('}') {
		return nil, false, errMetadataShape
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) || status != "success" || !data {
		return nil, false, errMetadataShape
	}
	return values, cut, nil
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
