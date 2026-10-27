package config

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// Settings S2 §3: the cluster entry writers change only what was asked,
// keep every other byte, refuse what they cannot edit line by line, and
// write only the planned file version through slice 0's policy.

func strp(s string) *string { return &s }

func editFleet(t *testing.T, doc, name string, edit ClusterEdit) (string, error) {
	t.Helper()
	path := writeFleet(t, doc)
	v, err := FileVersion(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = EditClusterAt(path, v, name, edit)
	return readFleet(t, path), err
}

func removeFleet(t *testing.T, doc, name string) (string, error) {
	t.Helper()
	path := writeFleet(t, doc)
	v, err := FileVersion(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = RemoveClusterAt(path, v, name)
	return readFleet(t, path), err
}

const editDoc = `# my fleet
clusters:
  # the dev cluster
  - name: dev # inline
    context: dev-ctx   # where it runs
    group: platform
    metrics:
      endpoint: https://m.example # metrics
    tags:
      region: westeurope # the region
      team: core

  - name: stg
    context: stg
ai:
  provider: off
`

func TestAnEditChangesOnlyItsFields(t *testing.T) {
	tags := map[string]string{"region": "northeurope", "team": "core"}
	got, err := editFleet(t, editDoc, "dev", ClusterEdit{Context: strp("dev-2"), Group: strp(""), Environment: strp("dev"), Tags: &tags})
	if err != nil {
		t.Fatal(err)
	}
	want := `# my fleet
clusters:
  # the dev cluster
  - name: dev # inline
    context: dev-2   # where it runs
    environment: dev
    metrics:
      endpoint: https://m.example # metrics
    tags:
      region: northeurope
      team: core

  - name: stg
    context: stg
ai:
  provider: off
`
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestAnEditAddsFieldsAfterTheEntrysLastLineKey(t *testing.T) {
	yes := true
	tags := map[string]string{"region": "westeurope", "env": "stg"}
	got, err := editFleet(t, editDoc, "stg", ClusterEdit{Kubeconfig: strp("~/.kube/stg"), Protected: &yes, Tags: &tags})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(editDoc, "    context: stg\n", `    context: stg
    kubeconfig: ~/.kube/stg
    protected: true
    tags:
      env: stg
      region: westeurope
`, 1)
	if got != want {
		t.Fatalf("got:\n%s", got)
	}
	// and clears them again, line by line
	no := false
	path := writeFleet(t, got)
	v, _ := FileVersion(path)
	if _, err := EditClusterAt(path, v, "stg", ClusterEdit{Kubeconfig: strp(""), Protected: &no, Tags: &map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	if back := readFleet(t, path); back != editDoc {
		t.Fatalf("cleared:\n%s", back)
	}
}

// A value is quoted when YAML would not read it back as the same string.
func TestAnEditQuotesWhatYAMLWouldMisread(t *testing.T) {
	for value, want := range map[string]string{
		"arn:aws:eks:eu-west-1:123456789012:cluster/prd": "arn:aws:eks:eu-west-1:123456789012:cluster/prd",
		"user@cluster": "user@cluster",
		"true":         `"true"`,
		"0123":         `"0123"`,
		"1.5":          `"1.5"`,
		"a: b":         `"a: b"`,
		"# not":        `"# not"`,
		"null":         `"null"`,
	} {
		got, err := editFleet(t, "clusters:\n  - name: a\n    group: g\n", "a", ClusterEdit{Group: strp(value)})
		if err != nil {
			t.Fatalf("%q: %v", value, err)
		}
		if line := "    group: " + want + "\n"; !strings.Contains(got, line) {
			t.Fatalf("%q: got\n%s", value, got)
		}
	}
}

func TestAnEditKeepsLineEndingsAndDocumentMarkers(t *testing.T) {
	doc := "---\r\nclusters:\r\n  - name: a\r\n    context: a # here\r\n    tags:\r\n      region: we\r\n...\r\n"
	got, err := editFleet(t, doc, "a", ClusterEdit{Context: strp("b"), Environment: strp("dev"), Tags: &map[string]string{"region": "ne"}})
	if err != nil {
		t.Fatal(err)
	}
	want := "---\r\nclusters:\r\n  - name: a\r\n    context: b # here\r\n    environment: dev\r\n    tags:\r\n      region: ne\r\n...\r\n"
	if got != want {
		t.Fatalf("got %q", got)
	}
	// a file without a final newline
	got, err = editFleet(t, "clusters:\n  - name: a", "a", ClusterEdit{Group: strp("g")})
	if err != nil || got != "clusters:\n  - name: a\n    group: g\n" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestAnEditReplacesAnEmptyOrNullValue(t *testing.T) {
	for _, line := range []string{"    group:\n", "    group: # none yet\n", "    group: ~\n", "    group: null\n"} {
		got, err := editFleet(t, "clusters:\n  - name: a\n"+line, "a", ClusterEdit{Group: strp("g")})
		if err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		if !strings.HasPrefix(got, "clusters:\n  - name: a\n    group: g") {
			t.Fatalf("%q: got %q", line, got)
		}
	}
}

func TestAnEditRefusesWhatItCannotEditLineByLine(t *testing.T) {
	for name, c := range map[string]struct {
		doc, cluster string
		edit         ClusterEdit
		why          string
	}{
		"flow list":   {"clusters: [{name: a}, {name: b}]\n", "a", ClusterEdit{Group: strp("g")}, "inline ([...])"},
		"flow entry":  {"clusters:\n  - {name: a, context: a}\n", "a", ClusterEdit{Group: strp("g")}, "inline ({...})"},
		"anchor":      {"clusters:\n  - name: a\n    context: &c a-ctx\n  - name: b\n    context: *c\n", "a", ClusterEdit{Group: strp("g")}, "anchor"},
		"alias":       {"clusters:\n  - name: a\n    context: &c a-ctx\n  - name: b\n    context: *c\n", "b", ClusterEdit{Group: strp("g")}, "alias"},
		"multi-line":  {"clusters:\n  - name: a\n    context: \"a\n      b\"\n", "a", ClusterEdit{Context: strp("c")}, "several lines"},
		"plain lines": {"clusters:\n  - name: a\n    context: a\n      b\n", "a", ClusterEdit{Context: strp("c")}, "several lines"},
		"block value": {"clusters:\n  - name: a\n    context: >-\n      a\n", "a", ClusterEdit{Context: strp("c")}, "several lines"},
		"flow tags":   {"clusters:\n  - name: a\n    tags: {region: we}\n", "a", ClusterEdit{Tags: &map[string]string{"region": "ne"}}, "tags are written inline"},
		"dash line":   {"clusters:\n  - context: c\n    name: a\n", "a", ClusterEdit{Context: strp("")}, "shares the list dash's line"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := editFleet(t, c.doc, c.cluster, c.edit)
			var hand *HandEditError
			if !errors.As(err, &hand) || !strings.Contains(err.Error(), c.why) || !strings.Contains(err.Error(), "by hand") {
				t.Fatalf("got %v", err)
			}
			if got != c.doc {
				t.Fatal("the file changed")
			}
		})
	}
}

// What would not load is refused before anything is built: a duplicated
// key, a merge key Klyx does not read, tags that are not a mapping.
func TestAnEditRefusesAFileThatDoesNotLoad(t *testing.T) {
	for name, doc := range map[string]string{
		"duplicate": "clusters:\n  - name: a\n    group: x\n    group: y\n",
		"tags list": "clusters:\n  - name: a\n    tags: [a]\n",
		"secret":    "clusters:\n  - name: a\n    metrics:\n      endpoint: https://m.example\n      token: [s3cr3t-value\n",
	} {
		got, err := editFleet(t, doc, "a", ClusterEdit{Group: strp("g")})
		if err == nil || got != doc {
			t.Fatalf("%s: edited: %v", name, err)
		}
		if strings.Contains(err.Error(), "s3cr3t") || !strings.Contains(err.Error(), "does not load") {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

// The candidate checks refuse any change but the intended one: another
// cluster, another top-level key, a key added, the entry read back
// otherwise, or another entry removed.
func TestTheCandidateChecksRefuseAnyOtherChange(t *testing.T) {
	before := []byte("clusters:\n  - name: a\n    group: g\n  - name: b\nai:\n  provider: off\n")
	setGroup := func(m map[string]any) { m["group"] = "h" }
	for name, after := range map[string]string{
		"another cluster": "clusters:\n  - name: a\n    group: h\n  - name: c\nai:\n  provider: off\n",
		"another key":     "clusters:\n  - name: a\n    group: h\n  - name: b\nai:\n  provider: anthropic\n",
		"a key added":     "clusters:\n  - name: a\n    group: h\n  - name: b\nai:\n  provider: off\nextra: x\n",
		"read otherwise":  "clusters:\n  - name: a\n    group: h\n    context: x\n  - name: b\nai:\n  provider: off\n",
	} {
		if err := onlyEntryChanged(before, []byte(after), 0, setGroup); err == nil {
			t.Fatalf("%s: passed", name)
		}
	}
	if err := onlyEntryChanged(before, []byte("clusters:\n  - name: a\n    group: h\n  - name: b\nai:\n  provider: off\n"), 0, setGroup); err != nil {
		t.Fatalf("the intended change: %v", err)
	}
	if err := onlyEntryRemoved(before, []byte("clusters:\n  - name: a\n    group: g\nai:\n  provider: off\n"), 0); err == nil {
		t.Fatal("removing another entry passed")
	}
	if err := onlyEntryRemoved(before, []byte("clusters:\n  - name: b\nai:\n  provider: off\n"), 0); err != nil {
		t.Fatalf("the intended removal: %v", err)
	}
}

// The write runs the candidate check: a candidate that loads but changes
// another cluster is refused, and the file is left as it was.
func TestAClusterWriteRefusesACandidateThatFailsItsCheck(t *testing.T) {
	doc := "clusters:\n  - name: a\n  - name: b\n"
	path := writeFleet(t, doc)
	v, _ := FileVersion(path)
	_, err := writeEntryAt(path, v, "the edit", func([]byte) ([]byte, func(before, after []byte) error, error) {
		return []byte("clusters:\n  - name: a\n  - name: c\n"), func(before, after []byte) error {
			return onlyEntryChanged(before, after, 0, func(map[string]any) {})
		}, nil
	})
	if err == nil || !strings.Contains(err.Error(), "would change other settings") || readFleet(t, path) != doc {
		t.Fatalf("got %v", err)
	}
}

// At one line, a replacement is applied before an insertion, so the
// insertion lands above what replaced it, whatever order the ops come in.
func TestLineOpsInsertAboveAReplacementAtTheSameLine(t *testing.T) {
	lines := []string{"a\n", "b\n", "c\n"}
	got := string(applyOps(lines, []lineOp{{at: 1, ins: []string{"x\n"}}, {at: 1, del: 1, ins: []string{"y\n"}}}))
	if got != "a\nx\ny\nc\n" {
		t.Fatalf("got %q", got)
	}
}

// A comment indented inside an entry or a value belongs to it; one at the
// entry's column leads into what follows.
func TestCommentsInsideAnEntryGoWithIt(t *testing.T) {
	got, err := removeFleet(t, "clusters:\n  - name: a\n    context: a\n    # a's note\n  - name: b\n", "a")
	if err != nil || got != "clusters:\n  - name: b\n" {
		t.Fatalf("removed: %q, %v", got, err)
	}
	doc := "clusters:\n  - name: a\n    tags:\n      region: we\n      # team: core\n    # about b\n  - name: b\n"
	got, err = editFleet(t, doc, "a", ClusterEdit{Tags: &map[string]string{"region": "ne"}})
	if err != nil || got != "clusters:\n  - name: a\n    tags:\n      region: ne\n    # about b\n  - name: b\n" {
		t.Fatalf("tags: %q, %v", got, err)
	}
	doc = "clusters:\n  - name: a\n    context: a\n      # deeper\n    group: g\n"
	got, err = editFleet(t, doc, "a", ClusterEdit{Context: strp("b")})
	if err != nil || got != strings.Replace(doc, "context: a", "context: b", 1) {
		t.Fatalf("context: %q, %v", got, err)
	}
}

// An unchanged field written over several lines does not stop an edit of
// another field.
func TestAnUnchangedMultiLineFieldIsLeftAlone(t *testing.T) {
	doc := "clusters:\n  - name: a\n    context: \"a\n      b\"\n    group: g\n"
	got, err := editFleet(t, doc, "a", ClusterEdit{Group: strp("h"), Context: strp("a b")})
	if err != nil {
		t.Fatal(err)
	}
	if got != strings.Replace(doc, "group: g", "group: h", 1) {
		t.Fatalf("got %q", got)
	}
}

// S2 §6: the defaulted context, and protection inferred from it, after a
// field is cleared.
func TestAClearedFieldTakesItsDefault(t *testing.T) {
	path := writeFleet(t, "clusters:\n  - name: x\n    context: x-prd\n")
	if c, _ := Load(path); !c.Clusters[0].IsProtected() {
		t.Fatal("rig: the context infers protection")
	}
	v, _ := FileVersion(path)
	if _, err := EditClusterAt(path, v, "x", ClusterEdit{Context: strp("")}); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil || c.Clusters[0].Context != "x" || c.Clusters[0].IsProtected() {
		t.Fatalf("the cleared context: %+v %v", c.Clusters[0], err)
	}
	path = writeFleet(t, "clusters:\n  - name: prd-x\n    context: c\n    environment: prd\n")
	v, _ = FileVersion(path)
	if _, err := EditClusterAt(path, v, "prd-x", ClusterEdit{Environment: strp("")}); err != nil {
		t.Fatal(err)
	}
	if c, _ := Load(path); !c.Clusters[0].IsProtected() {
		t.Fatal("the name still infers protection")
	}
}

func TestAClusterWriteTakesOnlyItsPlannedVersion(t *testing.T) {
	doc := "clusters:\n  - name: a\n  - name: b\n"
	for name, write := range map[string]func(path, version string) (string, error){
		"edit":   func(p, v string) (string, error) { return EditClusterAt(p, v, "a", ClusterEdit{Group: strp("g")}) },
		"remove": func(p, v string) (string, error) { return RemoveClusterAt(p, v, "a") },
	} {
		t.Run(name, func(t *testing.T) {
			path := writeFleet(t, doc)
			v, _ := FileVersion(path)
			if _, err := write(path, ""); err == nil {
				t.Fatal("written without a version")
			}
			if err := os.WriteFile(path, []byte(doc+"# edited\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := write(path, v); !errors.Is(err, ErrFileChanged) {
				t.Fatalf("got %v", err)
			}
			v, _ = FileVersion(path)
			beforeReplace = func() { _ = os.WriteFile(path, []byte(doc+"# edited again\n"), 0o644) }
			defer func() { beforeReplace = nil }()
			if _, err := write(path, v); !errors.Is(err, ErrFleetChangedOnDisk) {
				t.Fatalf("got %v", err)
			}
			if got := readFleet(t, path); got != doc+"# edited again\n" {
				t.Fatalf("the outside edit was overwritten: %q", got)
			}
			beforeReplace = nil
			path = writeFleetMode(t, doc, 0o640)
			v, _ = FileVersion(path)
			nv, err := write(path, v)
			if err != nil {
				t.Fatal(err)
			}
			if now, _ := FileVersion(path); now != nv {
				t.Fatal("the returned version is not the written file's")
			}
			if st, _ := os.Stat(path); st.Mode().Perm() != 0o640 {
				t.Fatalf("mode %v", st.Mode().Perm())
			}
		})
	}
}

const removeDoc = `clusters:
  # dev, above
  - name: dev
    context: dev
    # inside dev
    metrics:
      endpoint: https://m.example

  # stg, above
  - name: stg
    context: stg

  - name: prd
    context: prd
# trailing comment
ai:
  provider: off
`

func TestARemovalTakesOnlyTheEntrysLines(t *testing.T) {
	for name, want := range map[string]string{
		"dev": `clusters:
  # dev, above
  # stg, above
  - name: stg
    context: stg

  - name: prd
    context: prd
# trailing comment
ai:
  provider: off
`,
		"stg": `clusters:
  # dev, above
  - name: dev
    context: dev
    # inside dev
    metrics:
      endpoint: https://m.example

  # stg, above

  - name: prd
    context: prd
# trailing comment
ai:
  provider: off
`,
		"prd": `clusters:
  # dev, above
  - name: dev
    context: dev
    # inside dev
    metrics:
      endpoint: https://m.example

  # stg, above
  - name: stg
    context: stg

# trailing comment
ai:
  provider: off
`,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := removeFleet(t, removeDoc, name)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("got:\n%s", got)
			}
		})
	}
}

func TestARemovalBetweenBlankSeparatedItemsLeavesOneBlankLine(t *testing.T) {
	doc := "clusters:\n  - name: a\n\n  - name: b\n\n  - name: c\nai:\n  provider: off\n"
	got, err := removeFleet(t, doc, "b")
	if err != nil || got != "clusters:\n  - name: a\n\n  - name: c\nai:\n  provider: off\n" {
		t.Fatalf("got %q, %v", got, err)
	}
	got, err = removeFleet(t, "---\nclusters:\n  - name: a\n  - name: b\n...\n", "b")
	if err != nil || got != "---\nclusters:\n  - name: a\n...\n" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestARemovalRefusesTheLastClusterAndWhatItCannotRemove(t *testing.T) {
	doc := "clusters:\n  - name: a\n"
	if got, err := removeFleet(t, doc, "a"); !errors.Is(err, ErrLastCluster) || got != doc {
		t.Fatalf("got %v", err)
	}
	for _, doc := range []string{
		"clusters: [{name: a}, {name: b}]\n",
		"clusters:\n  - name: a\n    context: &c a-ctx\n  - name: b\n    context: *c\n",
	} {
		got, err := removeFleet(t, doc, "a")
		var hand *HandEditError
		if !errors.As(err, &hand) || got != doc || !strings.Contains(err.Error(), "remove cluster \"a\" from fleet.yaml by hand") {
			t.Fatalf("%q: got %v", doc, err)
		}
	}
	if _, err := removeFleet(t, "clusters:\n  - name: a\n  - name: b\n", "c"); err == nil {
		t.Fatal("removed a cluster that is not there")
	}
}
