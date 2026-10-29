package fleet

import (
	"sort"
	"strings"

	"github.com/moomora/klyx/internal/alertmanager"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/metrics"
	"github.com/moomora/klyx/internal/safehttp"
	"github.com/moomora/klyx/internal/sources"
)

// Settings S3b §3.2-§3.4: what a Loki or metrics read says about itself -
// its lineage, and the scope sentence (S3a §3.5's form) where its data is
// shown. A lineage keeps its terms raw, as identity (its key is a digest);
// what is shown - the sentence and the scope words - is said without the
// reading credential, each part clipped and the whole bounded.

// sentenceMax bounds a scope sentence, as the Loki reader's.
const sentenceMax = 1024

// scopeWords is a match as display: label=value, in label order, each
// clipped and redacted as Alertmanager's are.
func scopeWords(m map[string]string, red safehttp.Redactor) string {
	words := make([]string, 0, len(m))
	for k, v := range alertmanager.DisplayMatch(m, red) {
		words = append(words, k+"="+v)
	}
	sort.Strings(words)
	return strings.Join(words, ", ")
}

// match is the subscription's effective match; none for scope: tenant.
func (t sourceTerms) match() map[string]string {
	if t.sub.ScopeTenant {
		return nil
	}
	return t.sub.Match
}

// lineage is a subscribed read's lineage under its terms.
func (t sourceTerms) lineage() sources.Lineage {
	return sources.Lineage{Kind: t.sub.Kind, Mode: config.AMSubscribed, Identity: t.key(), Installation: t.sub.Source.ID,
		InstRev: t.sub.InstRev, SubRev: t.sub.SubRev, Host: t.sub.Source.Host, HostConn: uint64(t.hostConn), Access: t.access,
		Tenant: t.sub.Tenant, Match: scopeWords(t.match(), safehttp.Redactor{}), CredRev: t.credRev}
}

// words is the subscription's effective match as display, without the
// credential red stands for: what its empty answer says (S3b §3.3).
func (t sourceTerms) words(red safehttp.Redactor) string { return scopeWords(t.match(), red) }

// sentence is a subscription's scope sentence: where its data comes from,
// and what makes it this cluster's ("from lgtm-we-metrics, hosted by prd,
// tenant ops, filtered by cluster=dev"), without the reading credential
// (red). display is a direct installation's endpoint.
func (t sourceTerms) sentence(display string, red safehttp.Redactor) string {
	src := t.sub.Source
	parts := []string{"from " + src.ID}
	switch {
	case src.Host != "":
		parts[0] += ", hosted by " + src.Host
	case display != "":
		parts[0] += " at " + display
	}
	if t.sub.Tenant != "" {
		parts = append(parts, "tenant "+alertmanager.Clip(red.Text(t.sub.Tenant), alertmanager.MaxLabelValue))
	}
	if t.sub.ScopeTenant {
		parts = append(parts, "everything in its tenant")
	} else {
		parts = append(parts, "filtered by "+scopeWords(t.sub.Match, red))
	}
	return alertmanager.Clip(red.Text(strings.Join(parts, ", ")), sentenceMax)
}

// legacyMetrics is a cluster's own metrics block's lineage, sentence and
// scope words, as resolved: its endpoint, its Service or the discovered
// one, with its tenant and match, said without the credential its
// transport carries; none when nothing is read.
func legacyMetrics(res metrics.Resolution, cfg config.MetricsConfig, rev uint64) (lin sources.Lineage, sentence, words string) {
	mode, where := "", ""
	switch res.Mode {
	case metrics.ModeExplicitEndpoint:
		mode, where = config.AMDirect, "from "+res.Source
	case metrics.ModeExplicitService:
		mode, where = config.AMService, "from the Service "+res.Source+" on this cluster"
	case metrics.ModeDiscovered:
		mode, where = config.AMDiscovered, "from the discovered Service "+res.Source+" on this cluster"
	default:
		return sources.Lineage{}, "", ""
	}
	red := res.Transport.Redactor()
	parts := []string{where}
	if cfg.Tenant != "" {
		parts = append(parts, "tenant "+alertmanager.Clip(red.Text(cfg.Tenant), alertmanager.MaxLabelValue))
	}
	words = scopeWords(cfg.Match, red)
	if words == "" {
		parts = append(parts, "unfiltered: everything there is read as this cluster's")
	} else {
		parts = append(parts, "filtered by "+words)
	}
	return sources.Lineage{Kind: config.EndpointMetrics, Mode: mode, Identity: string(res.Mode) + "|" + res.Source,
			Tenant: cfg.Tenant, Match: scopeWords(cfg.Match, safehttp.Redactor{}), CredRev: rev},
		alertmanager.Clip(red.Text(strings.Join(parts, ", ")), sentenceMax), words
}

// noData is what an empty read under a scope says (S3b §3.3): "no data for
// dev under cluster=dev"; "" for a read with no scope (words, its match as
// display, without the credential), whose empty answer is said as before.
func noData(cluster, words string) string {
	if words == "" {
		return ""
	}
	return "no data for " + cluster + " under " + words
}
