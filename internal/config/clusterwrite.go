package config

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Settings S2 §3: the cluster entry writers. An edit writes an entry's
// changed S2 fields only, line by line, finding them in the parsed node
// tree; a removal takes the entry's lines out. A shape they cannot change
// without reformatting the person's file is refused, saying to edit it by
// hand and why. The candidate is checked before it is written - it loads
// strictly, and decoded, only the intended change was made - and is written
// against the plan's file version through slice 0's write policy (the
// symlink, the mode, and the compare-and-swap).

// ClusterEdit is a change to one cluster entry's S2 fields. A nil field is
// unchanged. A field set to its zero value ("", false, no tags) is cleared:
// its line goes. Region is tags.region.
type ClusterEdit struct {
	Context     *string
	Kubeconfig  *string
	Group       *string
	Environment *string
	Protected   *bool
	Tags        *map[string]string
}

// HandEditError is a cluster entry the writers will not change: the
// person edits it by hand, and Why says what stops Klyx.
type HandEditError struct {
	Cluster string
	Remove  bool
	Why     string
}

func (e *HandEditError) Error() string {
	if e.Remove {
		return fmt.Sprintf("remove cluster %q from fleet.yaml by hand: %s", e.Cluster, e.Why)
	}
	return fmt.Sprintf("edit cluster %q in fleet.yaml by hand: %s", e.Cluster, e.Why)
}

// ErrLastCluster: Klyx cannot remove the fleet's only cluster.
var ErrLastCluster = errors.New("klyx needs at least one cluster in fleet.yaml")

// EditClusterAt writes edit to the named cluster's entry, against the file
// version a plan was made from (any other is ErrFileChanged). It returns
// the written file's version.
func EditClusterAt(path, version, name string, edit ClusterEdit) (string, error) {
	return writeEntryAt(path, version, "the cluster's settings", func(doc []byte) ([]byte, func(before, after []byte) error, error) {
		return editEntry(doc, name, edit)
	})
}

// RemoveClusterAt removes the named cluster's entry, against the file
// version a plan was made from. The last cluster is refused.
func RemoveClusterAt(path, version, name string) (string, error) {
	return writeEntryAt(path, version, "the removal", func(doc []byte) ([]byte, func(before, after []byte) error, error) {
		return removeEntry(doc, name)
	})
}

// writeEntryAt is the write both writers share: under the fleet write
// lock, the file must be the planned version and load; build makes the
// candidate and the check that only the intended change was made; the
// candidate must load strictly and pass it; then it replaces the file only
// if the file is still what was read (slice 0's policy).
func writeEntryAt(path, version, what string, build func([]byte) ([]byte, func(before, after []byte) error, error)) (string, error) {
	if version == "" {
		return "", errors.New("a cluster entry is written only against the file version it was planned from")
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
	if _, err := decode(original); err != nil {
		return "", fmt.Errorf("fleet config %q does not load, so %s was not written: %w", path, what, err)
	}
	candidate, check, err := build(original)
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
		return "", fmt.Errorf("%s failed validation (file unchanged): %w", what, err)
	}
	if err := check(original, candidate); err != nil {
		return "", fmt.Errorf("%s would change other settings (file unchanged): %w", what, err)
	}
	if err := replaceUnchanged(path, target, mode, tmpPath, original, true); err != nil {
		return "", err
	}
	return versionOf(candidate), nil
}

// entryAt is a cluster entry in the parsed document, with the lines around
// it.
type entryAt struct {
	lines    []string // the document's lines, each with its line ending
	clusters *yaml.Node
	item     *yaml.Node
	index    int
	dash     int // 0-based line of the item's "-"
	dashCol  int // 0-based column of the "-"
	end      int // 0-based last line of the entry's content
}

func findEntry(doc []byte, name string, remove bool) (*entryAt, error) {
	hand := func(why string) error { return &HandEditError{Cluster: name, Remove: remove, Why: why} }
	var root yaml.Node
	if err := unmarshalFleet(doc, &root); err != nil {
		return nil, shown(err)
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("fleet.yaml is not a mapping")
	}
	var clusters *yaml.Node
	top := root.Content[0]
	for i := 0; i+1 < len(top.Content); i += 2 {
		if top.Content[i].Value == "clusters" {
			clusters = top.Content[i+1]
		}
	}
	switch {
	case clusters == nil || clusters.Kind == yaml.AliasNode:
		return nil, hand("the clusters list is not written out in the file")
	case clusters.Kind != yaml.SequenceNode:
		return nil, hand("the clusters field is not a list")
	case clusters.Style&yaml.FlowStyle != 0:
		return nil, hand("the clusters list is written inline ([...])")
	case clusters.Anchor != "":
		return nil, hand("the clusters list carries a YAML anchor")
	}
	e := &entryAt{lines: splitLines(doc), clusters: clusters, index: -1}
	for i, it := range clusters.Content {
		if n := mapValue(it, "name"); n != nil && n.Kind == yaml.ScalarNode && n.Value == name {
			if e.item != nil {
				return nil, hand("more than one entry has this name")
			}
			e.item, e.index = it, i
		}
	}
	if e.item == nil {
		return nil, fmt.Errorf("cluster %q is not in fleet.yaml", name)
	}
	if e.item.Kind != yaml.MappingNode {
		return nil, hand("the entry is not a mapping")
	}
	if why := yamlReferences(e.item); why != "" {
		return nil, hand(why)
	}
	var ok bool
	if e.dash, e.dashCol, ok = dashOf(e.lines, e.item); !ok {
		return nil, hand("Klyx cannot find the entry's list dash")
	}
	e.end = contentEnd(e.lines, maxLine(e.item)-1, e.dashCol)
	return e, nil
}

// yamlReferences says why a node's subtree cannot be edited line by line:
// an anchor another node may alias, an alias, or a merge key.
func yamlReferences(n *yaml.Node) string {
	switch {
	case n.Anchor != "":
		return "it carries a YAML anchor (&" + n.Anchor + ")"
	case n.Kind == yaml.AliasNode:
		return "it uses a YAML alias (*" + n.Value + ")"
	}
	if n.Kind == yaml.MappingNode {
		for i := 0; i < len(n.Content); i += 2 {
			if n.Content[i].Value == "<<" && n.Content[i].Tag == "!!merge" {
				return "it uses a YAML merge key (<<)"
			}
		}
	}
	for _, c := range n.Content {
		if why := yamlReferences(c); why != "" {
			return why
		}
	}
	return ""
}

func splitLines(doc []byte) []string {
	lines := strings.SplitAfter(string(doc), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// dashOf finds the item's "-": on the item's first line, before its first
// key, or alone on an earlier line.
func dashOf(lines []string, item *yaml.Node) (line, col int, ok bool) {
	l := item.Line - 1
	if l < 0 || l >= len(lines) {
		return 0, 0, false
	}
	first := byteCol(lines[l], item.Column-1)
	if c := strings.LastIndex(lines[l][:first], "-"); c >= 0 && strings.TrimSpace(lines[l][:c]) == "" {
		return l, c, true
	}
	for j := l - 1; j >= 0; j-- {
		t := strings.TrimSpace(lines[j])
		switch {
		case t == "" || strings.HasPrefix(t, "#"):
			continue
		case t == "-":
			return j, strings.Index(lines[j], "-"), true
		}
		return 0, 0, false
	}
	return 0, 0, false
}

// byteCol is the byte offset of a 0-based character column in line.
func byteCol(line string, col int) int {
	off := 0
	for i := 0; i < col && off < len(line); i++ {
		_, n := utf8.DecodeRuneInString(line[off:])
		off += n
	}
	return off
}

// maxLine is the last line any node in n's subtree starts on (1-based).
func maxLine(n *yaml.Node) int {
	m := n.Line
	for _, c := range n.Content {
		if l := maxLine(c); l > m {
			m = l
		}
	}
	return m
}

// contentEnd is the last line that belongs below the column of an item's
// dash, from line from (0-based): the lines that follow and are blank or
// indented deeper continue it - a block scalar, a multi-line value, a
// comment inside the entry - until a line at or left of the dash (another
// item, a top-level key or comment, a document marker). Trailing blank
// lines belong to what follows.
func contentEnd(lines []string, from, dashCol int) int {
	end := from
	for j := from + 1; j < len(lines); j++ {
		if strings.TrimSpace(lines[j]) == "" {
			continue
		}
		if indentOf(lines[j]) <= dashCol {
			break
		}
		end = j
	}
	return end
}

func indentOf(line string) int { return len(line) - len(strings.TrimLeft(line, " ")) }

func isBlankOrComment(line string) bool {
	t := strings.TrimSpace(line)
	return t == "" || strings.HasPrefix(t, "#")
}

// docEOL is the document's line ending: its first line's, or "\n".
func docEOL(lines []string) string {
	for _, l := range lines {
		if eol := eolOf(l); eol != "" {
			return eol
		}
	}
	return "\n"
}

func eolOf(line string) string {
	switch {
	case strings.HasSuffix(line, "\r\n"):
		return "\r\n"
	case strings.HasSuffix(line, "\n"):
		return "\n"
	}
	return ""
}

// lineOp replaces del lines at at with ins.
type lineOp struct {
	at, del int
	ins     []string
}

func applyOps(lines []string, ops []lineOp) []byte {
	// bottom up; at one line, a replacement before an insertion, so the
	// insertion lands above what replaced it
	sort.SliceStable(ops, func(i, j int) bool {
		if ops[i].at != ops[j].at {
			return ops[i].at > ops[j].at
		}
		return ops[i].del > ops[j].del
	})
	out := append([]string(nil), lines...)
	for _, op := range ops {
		tail := append([]string(nil), out[op.at+op.del:]...)
		out = append(append(out[:op.at], op.ins...), tail...)
	}
	return []byte(strings.Join(out, ""))
}

// the S2 fields, in the order an inserted line takes
var editKeys = []string{"context", "kubeconfig", "group", "environment", "protected", "tags"}

// editEntry builds the candidate for an edit, and its check.
func editEntry(doc []byte, name string, edit ClusterEdit) ([]byte, func(before, after []byte) error, error) {
	e, err := findEntry(doc, name, false)
	if err != nil {
		return nil, nil, err
	}
	hand := func(why string) error { return &HandEditError{Cluster: name, Why: why} }
	it := e.item
	if it.Style&yaml.FlowStyle != 0 {
		return nil, nil, hand("the entry is written inline ({...})")
	}
	keys := map[string]int{}
	for i := 0; i+1 < len(it.Content); i += 2 {
		k := it.Content[i].Value
		if _, dup := keys[k]; dup {
			return nil, nil, hand("the key " + k + " appears twice")
		}
		keys[k] = i
	}
	keyCol := it.Content[0].Column - 1
	// span is the lines a key's value takes: from its key line to the last
	// line before the next key, or the entry's end, less the blank lines
	// and the comments at the key's column or left of it, which lead into
	// what follows. A comment indented deeper is inside the value.
	span := func(i int) (first, last int) {
		first = it.Content[i].Line - 1
		next := e.end + 1
		if i+2 < len(it.Content) {
			next = it.Content[i+2].Line - 1
		}
		last = next - 1
		for last > first && (strings.TrimSpace(e.lines[last]) == "" || isBlankOrComment(e.lines[last]) && indentOf(e.lines[last]) <= keyCol) {
			last--
		}
		return first, last
	}
	oneLine := func(i int) bool {
		first, last := span(i)
		for j := first + 1; j <= last; j++ {
			if !isBlankOrComment(e.lines[j]) {
				return false // a value continued on the lines below
			}
		}
		v := it.Content[i+1]
		return v.Kind == yaml.ScalarNode && v.Style&(yaml.LiteralStyle|yaml.FoldedStyle) == 0 &&
			(v.Line == it.Content[i].Line || v.Tag == "!!null" && v.Value == "")
	}

	want := map[string]any{} // key -> the value written, or nil to clear
	set := func(key string, v *string) {
		if v != nil {
			if *v == "" {
				want[key] = nil
			} else {
				want[key] = *v
			}
		}
	}
	set("context", edit.Context)
	set("kubeconfig", edit.Kubeconfig)
	set("group", edit.Group)
	set("environment", edit.Environment)
	if edit.Protected != nil {
		if *edit.Protected {
			want["protected"] = true
		} else {
			want["protected"] = nil
		}
	}
	if edit.Tags != nil {
		if len(*edit.Tags) == 0 {
			want["tags"] = nil
		} else {
			want["tags"] = *edit.Tags
		}
	}

	if len(want) == 0 {
		return nil, nil, errors.New("the edit changes nothing")
	}
	eol := docEOL(e.lines)
	var ops []lineOp
	var inserts []string // the keys to add, in editKeys order
	for _, key := range editKeys {
		v, ok := want[key]
		if !ok {
			continue
		}
		i, present := keys[key]
		if !present {
			if v != nil {
				inserts = append(inserts, key)
			}
			continue
		}
		k, val := it.Content[i], it.Content[i+1]
		first, last := span(i)
		if key == "tags" {
			switch {
			case val.Kind == yaml.MappingNode && val.Style&yaml.FlowStyle != 0 && len(val.Content) > 0:
				return nil, nil, hand("its tags are written inline ({...})")
			case val.Kind != yaml.MappingNode && !(val.Kind == yaml.ScalarNode && val.Tag == "!!null"):
				return nil, nil, hand("its tags are not a mapping")
			case first == e.dash:
				return nil, nil, hand("its tags start on the list dash's line")
			}
			op := lineOp{at: first, del: last - first + 1}
			if v != nil {
				op.ins = tagsBlock(v.(map[string]string), strings.Repeat(" ", keyCol), eol)
			}
			ops = append(ops, op)
			continue
		}
		if s, ok := v.(string); ok && val.Kind == yaml.ScalarNode && val.Tag == "!!str" && val.Value == s {
			continue // already this value
		}
		if b, ok := v.(bool); ok && val.Kind == yaml.ScalarNode && val.Tag == "!!bool" && val.Value == strconv.FormatBool(b) {
			continue
		}
		if !oneLine(i) {
			return nil, nil, hand("its " + key + " is written over several lines")
		}
		if v == nil {
			if first == e.dash {
				return nil, nil, hand("its " + key + " shares the list dash's line")
			}
			ops = append(ops, lineOp{at: first, del: 1})
			continue
		}
		line, err := replaceValue(e.lines[first], k, val, scalarFor(v))
		if err != nil {
			return nil, nil, hand(err.Error())
		}
		ops = append(ops, lineOp{at: first, del: 1, ins: []string{line}})
	}
	if len(inserts) > 0 {
		// after the entry's last key on a line of its own: never inside a
		// nested block that follows it
		after := -1
		for i := 0; i+1 < len(it.Content); i += 2 {
			if oneLine(i) {
				after = it.Content[i].Line - 1
			}
		}
		if after < 0 {
			return nil, nil, hand("Klyx cannot find a line in the entry to add a field after")
		}
		indent := strings.Repeat(" ", keyCol)
		if eolOf(e.lines[after]) == "" {
			e.lines[after] += eol // the last line of a file without a final newline
		}
		var ins []string
		for _, key := range inserts {
			if key == "tags" {
				ins = append(ins, tagsBlock(want[key].(map[string]string), indent, eol)...)
				continue
			}
			ins = append(ins, indent+key+": "+scalarFor(want[key])+eol)
		}
		ops = append(ops, lineOp{at: after + 1, ins: ins})
	}
	candidate := applyOps(e.lines, ops)
	check := func(before, after []byte) error {
		return onlyEntryChanged(before, after, e.index, func(m map[string]any) {
			for key, v := range want {
				switch v := v.(type) {
				case nil:
					delete(m, key)
				case map[string]string:
					t := map[string]any{}
					for k, s := range v {
						t[k] = s
					}
					m[key] = t
				default:
					m[key] = v
				}
			}
		})
	}
	return candidate, check, nil
}

// replaceValue rewrites a one-line scalar's value in its line, keeping the
// key, the indentation, any trailing comment and the line ending.
func replaceValue(line string, key, val *yaml.Node, text string) (string, error) {
	eol := eolOf(line)
	body := strings.TrimSuffix(line, eol)
	var start int
	if val.Tag == "!!null" && val.Value == "" {
		// an empty value: it starts after the key's colon
		k := byteCol(body, key.Column-1)
		c := strings.Index(body[k:], ":")
		if c < 0 {
			return "", fmt.Errorf("Klyx cannot find the %s value on its line", key.Value)
		}
		start = k + c + 1
		rest := body[start:]
		if t := strings.TrimLeft(rest, " \t"); t != "" && !strings.HasPrefix(t, "#") {
			return "", fmt.Errorf("Klyx cannot find the %s value on its line", key.Value)
		}
		comment := ""
		if i := strings.Index(rest, "#"); i >= 0 {
			comment = " " + rest[i:]
		}
		return body[:start] + " " + text + comment + eol, nil
	}
	start = byteCol(body, val.Column-1)
	n, ok := scalarEnd(body[start:])
	if !ok {
		return "", fmt.Errorf("its %s is written over several lines", key.Value)
	}
	rest := body[start+n:]
	if strings.TrimSpace(rest) == "" {
		rest = ""
	}
	return body[:start] + text + rest + eol, nil
}

// scalarEnd is the length of the scalar at the start of s: a quoted one to
// its closing quote, a plain one to a comment or the line's end. ok is
// false for a quoted scalar not closed on the line.
func scalarEnd(s string) (int, bool) {
	if s == "" {
		return 0, true
	}
	switch s[0] {
	case '\'':
		for i := 1; i < len(s); i++ {
			if s[i] == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					i++
					continue
				}
				return i + 1, true
			}
		}
		return 0, false
	case '"':
		for i := 1; i < len(s); i++ {
			switch s[i] {
			case '\\':
				i++
			case '"':
				return i + 1, true
			}
		}
		return 0, false
	}
	end := len(s)
	for i := 1; i < len(s); i++ {
		if s[i] == '#' && (s[i-1] == ' ' || s[i-1] == '\t') {
			end = i
			break
		}
	}
	return len(strings.TrimRight(s[:end], " \t")), true
}

// scalarFor is a value as Klyx writes it: true for protection, and a
// string plain when YAML reads it back as that same string, quoted
// otherwise.
func scalarFor(v any) string {
	switch v := v.(type) {
	case bool:
		return strconv.FormatBool(v)
	case string:
		return stringScalar(v)
	}
	panic(fmt.Sprintf("scalarFor: %T", v))
}

func stringScalar(s string) string {
	if plainReadsBack(s) {
		return s
	}
	return strconv.Quote(s)
}

func plainReadsBack(s string) bool {
	if s == "" || strings.TrimSpace(s) != s || strings.ContainsAny(s, "\n\r\t") ||
		strings.ContainsAny(s[:1], "-?:,[]{}#&*!|>'\"%@`") || strings.HasSuffix(s, ":") ||
		strings.Contains(s, ": ") || strings.Contains(s, " #") {
		return false
	}
	var m map[string]any
	if err := yaml.Unmarshal([]byte("v: "+s), &m); err != nil {
		return false
	}
	got, ok := m["v"].(string)
	return ok && got == s
}

// tagsBlock is tags in Klyx's form: one nested block, keys sorted.
func tagsBlock(tags map[string]string, indent, eol string) []string {
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := []string{indent + "tags:" + eol}
	for _, k := range keys {
		out = append(out, indent+"  "+stringScalar(k)+": "+stringScalar(tags[k])+eol)
	}
	return out
}

// removeEntry builds the candidate for a removal, and its check: the
// entry's lines from its "-" to its last line of content go, with the
// comments inside it; a comment directly above it stays, and so do the
// lines that lead into what follows.
func removeEntry(doc []byte, name string) ([]byte, func(before, after []byte) error, error) {
	e, err := findEntry(doc, name, true)
	if err != nil {
		return nil, nil, err
	}
	if len(e.clusters.Content) == 1 {
		return nil, nil, ErrLastCluster
	}
	del := e.end - e.dash + 1
	// one blank line between items, not two: blank lines after the entry go
	// when a blank line or the list's key precedes it
	if e.index == 0 || e.dash > 0 && strings.TrimSpace(e.lines[e.dash-1]) == "" {
		for e.dash+del < len(e.lines) && strings.TrimSpace(e.lines[e.dash+del]) == "" {
			del++
		}
	}
	candidate := applyOps(e.lines, []lineOp{{at: e.dash, del: del}})
	check := func(before, after []byte) error { return onlyEntryRemoved(before, after, e.index) }
	return candidate, check, nil
}

// onlyEntryChanged confirms that, decoded, every top-level key and every
// other cluster is unchanged, and the entry at index is the original with
// change applied.
func onlyEntryChanged(before, after []byte, index int, change func(map[string]any)) error {
	a, b, err := decodedPair(before, after)
	if err != nil {
		return err
	}
	ac, _ := a["clusters"].([]any)
	bc, _ := b["clusters"].([]any)
	if len(ac) != len(bc) || index >= len(ac) {
		return errors.New("the clusters list would change")
	}
	for i := range ac {
		if i == index {
			continue
		}
		if !reflect.DeepEqual(ac[i], bc[i]) {
			return fmt.Errorf("cluster entry %d would change", i)
		}
	}
	entry, _ := ac[index].(map[string]any)
	if entry == nil {
		return errors.New("the entry is not a mapping")
	}
	change(entry)
	if !reflect.DeepEqual(entry, bc[index]) {
		return errors.New("the entry would not read back as intended")
	}
	return nil
}

// onlyEntryRemoved confirms that, decoded, every top-level key is unchanged
// and the clusters list is the original without the entry at index.
func onlyEntryRemoved(before, after []byte, index int) error {
	a, b, err := decodedPair(before, after)
	if err != nil {
		return err
	}
	ac, _ := a["clusters"].([]any)
	bc, _ := b["clusters"].([]any)
	if index >= len(ac) {
		return errors.New("the clusters list would change")
	}
	want := append(append([]any{}, ac[:index]...), ac[index+1:]...)
	if !reflect.DeepEqual(want, bc) {
		return errors.New("a cluster other than the removed one would change")
	}
	return nil
}

func decodedPair(before, after []byte) (map[string]any, map[string]any, error) {
	var a, b map[string]any
	if err := unmarshalFleet(before, &a); err != nil {
		return nil, nil, fmt.Errorf("decode current config: %w", err)
	}
	if err := unmarshalFleet(after, &b); err != nil {
		return nil, nil, fmt.Errorf("decode new config: %w", err)
	}
	for k := range a {
		if k != "clusters" && !reflect.DeepEqual(a[k], b[k]) {
			return nil, nil, fmt.Errorf("field %s would change", describeKey(k))
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			return nil, nil, fmt.Errorf("field %s would be added", describeKey(k))
		}
	}
	return a, b, nil
}
