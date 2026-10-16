package crd

import (
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Shapes captured from the kind lab (2026-10-16): a CRD's table with its
// printer columns, and a built-in's with typed cells and wide columns.
const gitRepoTable = `{"kind":"Table","apiVersion":"meta.k8s.io/v1","metadata":{"continue":"tok"},
"columnDefinitions":[
 {"name":"Name","type":"string","format":"name","description":"Name must be unique within a namespace.","priority":0},
 {"name":"URL","type":"string","format":"","description":"Custom resource definition column (in JSONPath format): .spec.url","priority":0},
 {"name":"Age","type":"date","format":"","description":"Custom resource definition column (in JSONPath format): .metadata.creationTimestamp","priority":0},
 {"name":"Ready","type":"string","format":"","description":"Custom resource definition column (in JSONPath format): .status.conditions[?(@.type==\"Ready\")].status","priority":0},
 {"name":"Status","type":"string","format":"","description":"Custom resource definition column (in JSONPath format): .status.conditions[?(@.type==\"Ready\")].message","priority":0}],
"rows":[{"cells":["platform","ssh://git@git.example.com/fleet.git","6d","False","failed to checkout"],
 "object":{"kind":"PartialObjectMetadata","apiVersion":"meta.k8s.io/v1","metadata":{"name":"platform","namespace":"flux-system","creationTimestamp":"2026-10-10T15:30:38Z"}}}]}`

const deployTable = `{"kind":"Table","apiVersion":"meta.k8s.io/v1","metadata":{},
"columnDefinitions":[
 {"name":"Name","type":"string","format":"name","description":"","priority":0},
 {"name":"Ready","type":"string","format":"","description":"","priority":0},
 {"name":"Up-to-date","type":"integer","format":"","description":"","priority":0},
 {"name":"Available","type":"integer","format":"","description":"","priority":0},
 {"name":"Age","type":"string","format":"","description":"","priority":0},
 {"name":"Containers","type":"string","format":"","description":"","priority":1}],
"rows":[{"cells":["coredns","2/2",2,2,"6d4h","coredns"],
 "object":{"kind":"PartialObjectMetadata","apiVersion":"meta.k8s.io/v1","metadata":{"name":"coredns","namespace":"kube-system","creationTimestamp":"2026-10-10T11:13:26Z"}}}]}`

func decodeTable(t *testing.T, s string) *metav1.Table {
	t.Helper()
	tbl, err := DecodeTable([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return tbl
}

func TestFromTable_CRDColumns(t *testing.T) {
	page, err := FromTable(decodeTable(t, gitRepoTable), false)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, c := range page.Columns {
		names = append(names, c.Name)
	}
	if !reflect.DeepEqual(names, []string{"URL", "Ready", "Status"}) {
		t.Fatalf("columns %v: the name column and the creation-time Age are Klyx's own", names)
	}
	if page.Columns[1].ConditionType != "Ready" || page.Columns[0].ConditionType != "" || page.Columns[2].ConditionType != "" {
		t.Errorf("only the Ready status column is a condition: %+v", page.Columns)
	}
	if page.Columns[0].JSONPath != ".spec.url" || page.Continue != "tok" {
		t.Errorf("jsonPath %q continue %q", page.Columns[0].JSONPath, page.Continue)
	}
	it := page.Items[0]
	if it.Name != "platform" || it.Namespace != "flux-system" || it.Created.IsZero() {
		t.Errorf("metadata %+v", it)
	}
	if !reflect.DeepEqual(it.Cells, []string{"ssh://git@git.example.com/fleet.git", "False", "failed to checkout"}) {
		t.Errorf("cells %q", it.Cells)
	}
}

func TestFromTable_BuiltinTypedAndWide(t *testing.T) {
	page, err := FromTable(decodeTable(t, deployTable), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Columns) != 4 || page.Columns[1].Name != "Up-to-date" || !page.Columns[1].Numeric || page.Columns[3].Priority != 1 {
		t.Fatalf("columns %+v", page.Columns)
	}
	if !reflect.DeepEqual(page.Items[0].Cells, []string{"2/2", "2", "2", "coredns"}) {
		t.Errorf("cells %q", page.Items[0].Cells)
	}
}

func TestFromTable_KeepsAnAgeThatIsNotCreation(t *testing.T) {
	s := `{"kind":"Table","columnDefinitions":[{"name":"Name","type":"string","format":"name"},
	{"name":"Age","type":"date","description":"Custom resource definition column (in JSONPath format): .status.lastSeen"}],
	"rows":[{"cells":["a","3m"],"object":{"metadata":{"name":"a"}}}]}`
	page, err := FromTable(decodeTable(t, s), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Columns) != 1 || page.Items[0].Cells[0] != "3m" {
		t.Fatalf("an Age on another path is a column: %+v", page)
	}
}

func TestTableColumns_AgeIsDroppedOnlyWhereProven(t *testing.T) {
	s := `{"kind":"Table","columnDefinitions":[{"name":"Name","type":"string","format":"name"},
	{"name":"Age","type":"date","description":"When the widget was born."}],"rows":[]}`
	if cols, _ := TableColumns(decodeTable(t, s), false); len(cols) != 1 {
		t.Errorf("a CRD's Age with an author's description is unproven and stays: %+v", cols)
	}
	if cols, _ := TableColumns(decodeTable(t, s), true); len(cols) != 0 {
		t.Errorf("a built-in group's Age is creation time: %+v", cols)
	}
}

func TestTableColumns_ConditionProvenanceIsThePath(t *testing.T) {
	d := func(name, path string) string {
		return `{"name":"` + name + `","type":"string","description":"Custom resource definition column (in JSONPath format): ` + path + `"}`
	}
	s := `{"kind":"Table","columnDefinitions":[` + strings.Join([]string{
		d("Ready", ".spec.mode"),
		d("Ready", `.status.conditions[?(@.type==\"Stalled\")].status`),
		d("Synced", `.status.conditions[?(@.type=='Synced')].status`),
		d("Msg", `.status.conditions[?(@.type==\"Ready\")].message`),
	}, ",") + `],"rows":[]}`
	cols, _ := TableColumns(decodeTable(t, s), false)
	got := []string{}
	for _, c := range cols {
		got = append(got, c.ConditionType)
	}
	if !reflect.DeepEqual(got, []string{"", "Stalled", "Synced", ""}) {
		t.Fatalf("condition types %q", got)
	}
}

func TestDecodeTable_KeepsNumbersExact(t *testing.T) {
	s := `{"kind":"Table","columnDefinitions":[{"name":"Name","type":"string","format":"name"},{"name":"Bytes","type":"integer"}],
	"rows":[{"cells":["a",9007199254740993],"object":{"metadata":{"name":"a"}}}]}`
	page, err := FromTable(decodeTable(t, s), false)
	if err != nil {
		t.Fatal(err)
	}
	if page.Items[0].Cells[0] != "9007199254740993" {
		t.Fatalf("a large integer lost precision: %q", page.Items[0].Cells[0])
	}
}

func TestFromTable_RowWithoutObjectIsAnError(t *testing.T) {
	s := `{"kind":"Table","columnDefinitions":[{"name":"Name","type":"string","format":"name"}],"rows":[{"cells":["a"]}]}`
	if _, err := FromTable(decodeTable(t, s), false); err == nil {
		t.Fatal("a row with no metadata cannot name its object")
	}
}

func TestCellText(t *testing.T) {
	for in, want := range map[interface{}]string{nil: "", "x": "x", true: "true", 2.0: "2", 2.5: "2.5"} {
		if got := CellText(in); got != want {
			t.Errorf("CellText(%v) = %q, want %q", in, got, want)
		}
	}
	if got := CellText([]interface{}{"a", "b"}); got != `["a","b"]` {
		t.Errorf("a list %q", got)
	}
}
