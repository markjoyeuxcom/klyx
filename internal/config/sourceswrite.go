package config

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Settings S3a §5: an Observability plan's edits are built in memory into
// one disk candidate - the sources block by block replacement, each
// cluster's block of a kind by an entry edit - which must load strictly
// (and so resolve) and change only the intended blocks, and is written
// once, against the plan's file version, through slice 0's write policy.

// SourcesEdit is one Observability plan's edits to the fleet file. Sources,
// when set, replaces the top-level sources block; an empty list removes
// it. Blocks replaces a cluster's block of a kind - alertmanager, loki or
// metrics (Settings S3b §5) - with a subscription block; nil removes it.
type SourcesEdit struct {
	Sources *[]SourceConfig
	Blocks  map[BlockRef]*EndpointConfig
}

// BlockRef is one cluster's block of one kind.
type BlockRef struct{ Cluster, Kind string }

// blockKey is a kind's key in a cluster's entry.
func blockKey(kind string) (string, error) {
	switch kind {
	case EndpointAlertmanager, EndpointLoki, EndpointMetrics:
		return kind, nil
	}
	return "", fmt.Errorf("%s is not a kind of observability block", describeValue(kind))
}

// BuildSourcesCandidate is the disk candidate: doc with e applied, loaded
// strictly. A block Klyx cannot edit without reformatting the person's
// file is a *HandEditError; a candidate that would change anything else
// is refused.
func BuildSourcesCandidate(doc []byte, e SourcesEdit) ([]byte, *Config, error) {
	if e.Sources == nil && len(e.Blocks) == 0 {
		return nil, nil, errors.New("the plan changes nothing in fleet.yaml")
	}
	if _, err := decode(doc); err != nil {
		return nil, nil, fmt.Errorf("fleet.yaml does not load, so nothing was written: %w", err)
	}
	candidate := doc
	eol := docEOL(splitLines(doc))
	if e.Sources != nil {
		block, err := renderSources(*e.Sources, eol)
		if err != nil {
			return nil, nil, err
		}
		out, err := replaceTopLevelBlock(string(candidate), "sources", block)
		if err != nil {
			return nil, nil, err
		}
		candidate = []byte(out)
	}
	refs := make([]BlockRef, 0, len(e.Blocks))
	for ref := range e.Blocks {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Cluster != refs[j].Cluster {
			return refs[i].Cluster < refs[j].Cluster
		}
		return refs[i].Kind < refs[j].Kind
	})
	for _, ref := range refs {
		out, err := editEntryBlock(candidate, ref, e.Blocks[ref])
		if err != nil {
			return nil, nil, err
		}
		candidate = out
	}
	cfg, err := decode(candidate)
	if err != nil {
		return nil, nil, fmt.Errorf("the change failed validation (file unchanged): %w", err)
	}
	if err := onlySourcesChanged(doc, candidate, e); err != nil {
		return nil, nil, fmt.Errorf("the change would change other settings (file unchanged): %w", err)
	}
	return candidate, cfg, nil
}

// ReadFleetVersioned is the fleet file's bytes and their version, for a
// plan built from them.
func ReadFleetVersioned(path string) ([]byte, string, error) {
	raw, err := readFleetFile(path)
	if err != nil {
		return nil, "", err
	}
	return raw, versionOf(raw), nil
}

// ParseFleet is the configuration bytes hold, strictly loaded: a plan
// reads the file once, and plans from those bytes.
func ParseFleet(raw []byte) (*Config, error) { return decode(raw) }

// CheckSources is the runtime candidate's check (Settings S3a §5):
// sources valid against the clusters, each cluster's alertmanager block
// valid, and every cluster resolved, with the subscriptions' scopes
// disjoint.
func CheckSources(sources []SourceConfig, clusters []ClusterConfig) (map[string]AMResolution, error) {
	c := &Config{Sources: sources, Clusters: clusters}
	if err := c.validateSources(); err != nil {
		return nil, err
	}
	for _, cl := range clusters {
		for _, e := range cl.Endpoints() {
			spec := e.Spec
			if err := validateSubscription(cl.Name, e.Kind, &spec, e.LegacyToken); err != nil {
				return nil, err
			}
		}
	}
	// every kind resolves, so an Alertmanager plan never publishes a
	// candidate whose Loki or metrics resolution fails (S3b §2.2)
	if err := ResolveAll(sources, clusters); err != nil {
		return nil, err
	}
	return ResolveAlertmanager(sources, clusters)
}

// EditSourcesAt writes e against the file version a plan was made from
// (any other is ErrFileChanged), as one compare-and-swap. It returns the
// written file's version.
func EditSourcesAt(path, version string, e SourcesEdit) (string, error) {
	if version == "" {
		return "", errors.New("the sources are written only against the file version they were planned from")
	}
	fleetWriteMu.Lock()
	defer fleetWriteMu.Unlock()

	target, mode, _, err := fleetTarget(path)
	if err != nil {
		return "", err
	}
	original, err := readFleetFile(path)
	if err != nil {
		return "", fmt.Errorf("read fleet config %q: %w", path, err)
	}
	if versionOf(original) != version {
		return "", ErrFileChanged
	}
	candidate, _, err := BuildSourcesCandidate(original, e)
	if err != nil {
		return "", err
	}
	tmp, err := fleetTemp(target, mode)
	if err != nil {
		return "", fmt.Errorf("create temp config: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }() // best-effort temp cleanup
	if _, err := tmp.Write(candidate); err != nil {
		_ = tmp.Close() // already returning the write error
		return "", fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close temp config: %w", err)
	}
	if _, err := Load(tmpPath); err != nil {
		return "", fmt.Errorf("the change failed validation (file unchanged): %w", err)
	}
	if err := replaceUnchanged(path, target, mode, tmpPath, original, true); err != nil {
		return "", err
	}
	return versionOf(candidate), nil
}

// renderSources is the sources block in Klyx's form: each installation's
// fields in a fixed order, a field at its zero value left out.
func renderSources(sources []SourceConfig, eol string) (string, error) {
	if len(sources) == 0 {
		return "", nil
	}
	var b strings.Builder
	b.WriteString("sources:" + eol)
	for _, s := range sources {
		lines := []string{}
		add := func(indent, key, v string) {
			if v != "" {
				lines = append(lines, indent+key+": "+stringScalar(v)+eol)
			}
		}
		add("", "id", s.ID)
		add("", "kind", s.Kind)
		add("", "name", s.Name)
		add("", "host", s.Host)
		if r := s.ServiceRef; r != nil {
			lines = append(lines, "serviceRef:"+eol)
			add("  ", "namespace", r.Namespace)
			add("  ", "name", r.Name)
			add("  ", "port", r.Port)
			add("  ", "scheme", r.Scheme)
			add("  ", "path", r.Path)
		}
		if s.Discover {
			lines = append(lines, "discover: true"+eol)
		}
		add("", "endpoint", s.Endpoint)
		if s.TLSSkipVerify {
			lines = append(lines, "tlsSkipVerify: true"+eol)
		}
		if a := s.Auth; a != nil {
			lines = append(lines, "auth:"+eol)
			add("  ", "type", a.Type)
			add("  ", "username", a.Username)
		}
		add("", "tenant", s.Tenant)
		add("", "memberLabel", s.MemberLabel)
		if len(s.Serves) > 0 {
			lines = append(lines, "serves:"+eol)
			for _, sv := range s.Serves {
				switch {
				case sv.Group != "" && sv.Cluster == "":
					lines = append(lines, "  - group: "+stringScalar(sv.Group)+eol)
				case sv.Cluster != "" && sv.Group == "":
					lines = append(lines, "  - cluster: "+stringScalar(sv.Cluster)+eol)
				default:
					return "", errors.New("a serves entry names a group or a cluster, not both")
				}
			}
		}
		if len(lines) == 0 {
			return "", errors.New("an installation has no fields")
		}
		b.WriteString("  - " + lines[0])
		for _, l := range lines[1:] {
			b.WriteString("    " + l)
		}
	}
	return b.String(), nil
}

// subscriptionBlock is a cluster's block of a kind in Klyx's form, as
// lines at indent. Settings writes subscription blocks only: an
// installation, a scope, off, and the tenant and match over the
// installation's defaults; a legacy block stays the file's.
func subscriptionBlock(kind string, e *EndpointConfig, indent, eol string) ([]string, error) {
	key, err := blockKey(kind)
	if err != nil {
		return nil, err
	}
	if e.Endpoint != "" || e.ServiceRef != nil || e.Auth != nil || e.TLSSkipVerify {
		return nil, errors.New("only a subscription block is written here; an endpoint or Service stays the file's")
	}
	if e.Source == "" && !e.Off {
		return nil, errors.New("a subscription block names its installation, or is off")
	}
	in := indent + "  "
	out := []string{indent + key + ":" + eol}
	if e.Off {
		return append(out, in+"off: true"+eol), nil
	}
	out = append(out, in+"source: "+stringScalar(e.Source)+eol)
	if e.Scope != "" {
		out = append(out, in+"scope: "+stringScalar(e.Scope)+eol)
	}
	if e.Tenant != "" {
		out = append(out, in+"tenant: "+stringScalar(e.Tenant)+eol)
	}
	if len(e.Match) > 0 {
		keys := make([]string, 0, len(e.Match))
		for k := range e.Match {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out = append(out, in+"match:"+eol)
		for _, k := range keys {
			out = append(out, in+"  "+stringScalar(k)+": "+stringScalar(e.Match[k])+eol)
		}
	}
	return out, nil
}

// editEntryBlock replaces the named cluster's block of a kind with block,
// adds it, or removes it (nil), line by line in the entry.
func editEntryBlock(doc []byte, ref BlockRef, block *EndpointConfig) ([]byte, error) {
	name := ref.Cluster
	key, err := blockKey(ref.Kind)
	if err != nil {
		return nil, err
	}
	e, err := findEntry(doc, name, false)
	if err != nil {
		return nil, err
	}
	hand := func(why string) error { return &HandEditError{Cluster: name, Why: why} }
	it := e.item
	if it.Style&yaml.FlowStyle != 0 {
		return nil, hand("the entry is written inline ({...})")
	}
	at := -1
	for i := 0; i+1 < len(it.Content); i += 2 {
		if it.Content[i].Value == key {
			if at >= 0 {
				return nil, hand("the key " + key + " appears twice")
			}
			at = i
		}
	}
	keyCol := it.Content[0].Column - 1
	indent := strings.Repeat(" ", keyCol)
	eol := docEOL(e.lines)
	var lines []string
	if block != nil {
		if lines, err = subscriptionBlock(ref.Kind, block, indent, eol); err != nil {
			return nil, err
		}
	}
	if at >= 0 {
		first := it.Content[at].Line - 1
		if first == e.dash {
			return nil, hand("its " + key + " block starts on the list dash's line")
		}
		next := e.end + 1
		if at+2 < len(it.Content) {
			next = it.Content[at+2].Line - 1
		}
		last := next - 1
		// blank lines and comments at the key's column or left of it lead
		// into what follows
		for last > first && (strings.TrimSpace(e.lines[last]) == "" || isBlankOrComment(e.lines[last]) && indentOf(e.lines[last]) <= keyCol) {
			last--
		}
		return applyOps(e.lines, []lineOp{{at: first, del: last - first + 1, ins: lines}}), nil
	}
	if block == nil {
		return doc, nil // nothing to remove
	}
	// after the entry's content, so never inside a nested block
	end := e.end
	if eolOf(e.lines[end]) == "" {
		e.lines[end] += eol // the last line of a file without a final newline
	}
	return applyOps(e.lines, []lineOp{{at: end + 1, ins: lines}}), nil
}

// onlySourcesChanged confirms that, decoded, the candidate is the original
// with exactly the plan's blocks changed.
func onlySourcesChanged(before, after []byte, e SourcesEdit) error {
	var a, b map[string]any
	if err := unmarshalFleet(before, &a); err != nil {
		return fmt.Errorf("decode current config: %w", err)
	}
	if err := unmarshalFleet(after, &b); err != nil {
		return fmt.Errorf("decode new config: %w", err)
	}
	if e.Sources != nil {
		delete(a, "sources")
		if len(*e.Sources) > 0 {
			block, err := renderSources(*e.Sources, "\n")
			if err != nil {
				return err
			}
			var m map[string]any
			if err := yaml.Unmarshal([]byte(block), &m); err != nil {
				return err
			}
			a["sources"] = m["sources"]
		}
	}
	clusters, _ := a["clusters"].([]any)
	for ref, block := range e.Blocks {
		key, err := blockKey(ref.Kind)
		if err != nil {
			return err
		}
		found := false
		for _, c := range clusters {
			entry, _ := c.(map[string]any)
			if entry == nil || entry["name"] != ref.Cluster {
				continue
			}
			found = true
			delete(entry, key)
			if block != nil {
				lines, err := subscriptionBlock(ref.Kind, block, "", "\n")
				if err != nil {
					return err
				}
				var m map[string]any
				if err := yaml.Unmarshal([]byte(strings.Join(lines, "")), &m); err != nil {
					return err
				}
				entry[key] = m[key]
			}
		}
		if !found {
			return fmt.Errorf("cluster %s is not in fleet.yaml", describeValue(ref.Cluster))
		}
	}
	if !reflect.DeepEqual(a, b) {
		return errors.New("a setting outside the plan's blocks would change")
	}
	return nil
}
