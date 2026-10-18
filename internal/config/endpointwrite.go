package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// Coverage C5 §6: credential actions act on the fleet.yaml the person
// reviewed. A Snapshot is one read of the file; an endpoint's revision is a
// hash of what the person saw of it; an action whose revision no longer
// matches, or a migration whose file changed after its snapshot, writes
// nothing.

// ErrEndpointChanged: the endpoint changed in fleet.yaml since the person
// looked at it; nothing was written.
var ErrEndpointChanged = errors.New("this endpoint changed in fleet.yaml since you looked at it; review it and try again")

// ErrFileChangedSinceSnapshot: fleet.yaml changed between the snapshot an
// action read and its write; the newer file was left untouched.
var ErrFileChangedSinceSnapshot = errors.New("fleet.yaml changed while Klyx was updating it; the newer file was left untouched")

// Snapshot is one read of the fleet file: its bytes, mode and decoded
// configuration.
type Snapshot struct {
	Path   string
	Bytes  []byte
	Mode   os.FileMode
	Config *Config
}

// ReadSnapshot reads the fleet file once.
func ReadSnapshot(path string) (Snapshot, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read fleet config %q: %w", path, err)
	}
	st, err := os.Stat(path)
	if err != nil {
		return Snapshot{}, fmt.Errorf("stat fleet config %q: %w", path, err)
	}
	cfg, err := loadBytes(b)
	if err != nil {
		return Snapshot{}, fmt.Errorf("invalid config %q: %w", path, err)
	}
	return Snapshot{Path: path, Bytes: b, Mode: st.Mode().Perm(), Config: cfg}, nil
}

func loadBytes(b []byte) (*Config, error) {
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Endpoint finds a cluster's endpoint of a kind in the snapshot.
func (s Snapshot) Endpoint(cluster, kind string) (NamedEndpoint, *ClusterConfig, bool) {
	for i := range s.Config.Clusters {
		cl := &s.Config.Clusters[i]
		if cl.Name != cluster {
			continue
		}
		for _, e := range cl.Endpoints() {
			if e.Kind == kind {
				return e, cl, true
			}
		}
	}
	return NamedEndpoint{}, nil, false
}

// EndpointRevision is the endpoint's opaque revision in the snapshot: a hash
// of the endpoint's configuration as the person saw it - the spec, a hash of
// a legacy token, and the file's mode. "" when there is no such endpoint.
func (s Snapshot) EndpointRevision(cluster, kind string) string {
	e, cl, ok := s.Endpoint(cluster, kind)
	if !ok {
		return ""
	}
	spec, _ := json.Marshal(e.Spec)
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s\x00%s\x00%s\x00%o\x00", cluster, kind, spec, s.Mode) // a hash never fails to write
	if kind == EndpointMetrics && cl.Metrics != nil && cl.Metrics.Token != "" {
		t := sha256.Sum256([]byte(cl.Metrics.Token))
		h.Write(t[:])
	}
	return hex.EncodeToString(h.Sum(nil))[:24]
}

// MoveMetricsTokenToAuth rewrites the cluster's metrics.token line in the
// snapshot's bytes as auth: {type: bearer} (or removes it when auth is
// already set), checks that nothing else changes, and replaces fleet.yaml
// only if it still holds the snapshot's exact bytes and mode (C5 §6). The
// candidate is prepared in a private temporary file and given the
// snapshot's mode before the rename.
func MoveMetricsTokenToAuth(snap Snapshot, cluster string) (*Config, error) {
	fleetWriteMu.Lock()
	defer fleetWriteMu.Unlock()

	candidate, err := tokenLineEdit(snap.Bytes, cluster)
	if err != nil {
		return nil, err
	}
	got, err := loadBytes(candidate)
	if err != nil {
		return nil, fmt.Errorf("the edited config failed validation (file unchanged): %w", err)
	}
	if err := onlyTokenMoved(snap.Bytes, candidate, cluster); err != nil {
		return nil, fmt.Errorf("the edit would change other settings (file unchanged): %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(snap.Path), ".fleet-*.yaml") // 0600: the token is still plaintext here
	if err != nil {
		return nil, fmt.Errorf("create temp config: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(candidate); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("close temp config: %w", err)
	}
	if err := os.Chmod(tmpPath, snap.Mode); err != nil {
		return nil, fmt.Errorf("set the temp config's mode: %w", err)
	}
	if beforeReplace != nil {
		beforeReplace()
	}
	cur, err := os.ReadFile(snap.Path)
	if err != nil {
		return nil, fmt.Errorf("re-read fleet config: %w", err)
	}
	st, err := os.Stat(snap.Path)
	if err != nil {
		return nil, fmt.Errorf("stat fleet config: %w", err)
	}
	if !bytes.Equal(cur, snap.Bytes) || st.Mode().Perm() != snap.Mode {
		return nil, ErrFileChangedSinceSnapshot
	}
	if err := os.Rename(tmpPath, snap.Path); err != nil {
		return nil, fmt.Errorf("replace fleet config: %w", err)
	}
	return got, nil
}

// tokenLineEdit finds the cluster's metrics.token key in the document and
// replaces its line. Only a plain one-line `token: <value>` is edited; any
// other shape is left for the person to edit by hand.
func tokenLineEdit(doc []byte, cluster string) ([]byte, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(doc, &root); err != nil {
		return nil, err
	}
	metrics := clusterMetricsNode(&root, cluster)
	if metrics == nil {
		return nil, fmt.Errorf("cluster %q has no metrics block", cluster)
	}
	var tokenKey, tokenVal *yaml.Node
	hasAuth := false
	for i := 0; i+1 < len(metrics.Content); i += 2 {
		switch metrics.Content[i].Value {
		case "token":
			tokenKey, tokenVal = metrics.Content[i], metrics.Content[i+1]
		case "auth":
			hasAuth = true
		}
	}
	if tokenKey == nil {
		return nil, fmt.Errorf("cluster %q has no metrics.token", cluster)
	}
	if metrics.Style&yaml.FlowStyle != 0 || tokenVal.Kind != yaml.ScalarNode || tokenVal.Line != tokenKey.Line ||
		tokenVal.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		return nil, fmt.Errorf("cluster %q: metrics.token is not a plain one-line value; move it by hand", cluster)
	}
	lines := strings.SplitAfter(string(doc), "\n")
	i := tokenKey.Line - 1
	if i < 0 || i >= len(lines) || !strings.HasPrefix(strings.TrimLeft(lines[i], " "), "token:") {
		return nil, fmt.Errorf("cluster %q: metrics.token is not on a line of its own; move it by hand", cluster)
	}
	repl := ""
	if !hasAuth {
		nl := ""
		if strings.HasSuffix(lines[i], "\n") {
			nl = "\n"
		}
		repl = strings.Repeat(" ", tokenKey.Column-1) + "auth: {type: bearer}" + nl
	}
	lines[i] = repl
	return []byte(strings.Join(lines, "")), nil
}

func clusterMetricsNode(root *yaml.Node, cluster string) *yaml.Node {
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return nil
	}
	top := root.Content[0]
	clusters := mapValue(top, "clusters")
	if clusters == nil || clusters.Kind != yaml.SequenceNode {
		return nil
	}
	for _, item := range clusters.Content {
		if n := mapValue(item, "name"); n != nil && n.Value == cluster {
			if m := mapValue(item, "metrics"); m != nil && m.Kind == yaml.MappingNode {
				return m
			}
		}
	}
	return nil
}

func mapValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// onlyTokenMoved confirms that, decoded, the only change is the cluster's
// metrics.token gone and metrics.auth set to bearer.
func onlyTokenMoved(before, after []byte, cluster string) error {
	var a, b map[string]any
	if err := yaml.Unmarshal(before, &a); err != nil {
		return fmt.Errorf("decode current config: %w", err)
	}
	if err := yaml.Unmarshal(after, &b); err != nil {
		return fmt.Errorf("decode new config: %w", err)
	}
	cls, _ := a["clusters"].([]any)
	moved := false
	for _, c := range cls {
		cm, _ := c.(map[string]any)
		if cm == nil || cm["name"] != cluster {
			continue
		}
		mm, _ := cm["metrics"].(map[string]any)
		if mm == nil {
			continue
		}
		delete(mm, "token")
		if _, ok := mm["auth"]; !ok {
			mm["auth"] = map[string]any{"type": "bearer"}
		}
		moved = true
	}
	if !moved || !reflect.DeepEqual(a, b) {
		return errors.New("a setting other than this cluster's metrics.token would change")
	}
	return nil
}
