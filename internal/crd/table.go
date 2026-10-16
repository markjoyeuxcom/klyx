package crd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The server's table (The Line slice 15a). Asked for with
// TableAccept, the API server evaluates a kind's printer columns itself -
// a CRD's additionalPrinterColumns, a built-in's own printer - exactly as
// kubectl get prints them: the first JSONPath match, integer, number and
// boolean coercion, dates as relative age at the read, and each column's
// priority (0 is the default view, 1 and above is -o wide).

// TableAccept asks for a meta.k8s.io/v1 Table, falling back to plain JSON
// when the server cannot render one (the fallback is detected, not trusted).
const TableAccept = "application/json;as=Table;v=v1;g=meta.k8s.io,application/json"

// crdColumnDescription is the description the CRD table convertor gives a
// printer column whose author wrote none; the JSONPath follows it.
const crdColumnDescription = "Custom resource definition column (in JSONPath format): "

// Column is one printer column the list or the inspector can draw.
type Column struct {
	Name        string
	Type        string // integer, number, string, boolean, date
	Format      string
	Description string
	// JSONPath is known only when the server's description carries it.
	JSONPath string
	Priority int32
	Numeric  bool
	// ConditionType is set only when the JSONPath proves the column is a
	// condition's status (.status.conditions[?(@.type=="T")].status): the
	// type comes from the path, never from the column's name (design review
	// 1). A list may mark rows from it; any other column stays plain.
	ConditionType string
}

// InstancePage is one page of instances. Columns is nil for a page read
// without a table (the hand-written lists, or a server that cannot render
// one); then each item's Cells is nil too.
type InstancePage struct {
	Items    []InstanceMeta
	Continue string
	Columns  []Column
}

// BuiltinGroups are the API groups the kube-apiserver serves itself. Their
// printers' Age column is always creation time; a CRD's Age is creation
// time only when its JSONPath says so.
var BuiltinGroups = map[string]bool{
	"": true, "apps": true, "batch": true, "autoscaling": true, "policy": true,
	"networking.k8s.io": true, "rbac.authorization.k8s.io": true, "storage.k8s.io": true,
	"scheduling.k8s.io": true, "coordination.k8s.io": true, "node.k8s.io": true,
	"discovery.k8s.io": true, "certificates.k8s.io": true, "admissionregistration.k8s.io": true,
	"apiextensions.k8s.io": true, "apiregistration.k8s.io": true, "flowcontrol.apiserver.k8s.io": true,
	"events.k8s.io": true, "resource.k8s.io": true, "storagemigration.k8s.io": true,
	"internal.apiserver.k8s.io": true,
}

var conditionStatusPath = regexp.MustCompile(`^\.status\.conditions\[\?\(@\.type\s*==\s*(?:"([^"]+)"|'([^']+)')\)\]\.status$`)

// conditionTypeOf proves a column is a condition's status from its JSONPath.
func conditionTypeOf(path string) string {
	m := conditionStatusPath.FindStringSubmatch(strings.TrimSpace(path))
	if m == nil {
		return ""
	}
	if m[1] != "" {
		return m[1]
	}
	return m[2]
}

// DecodeTable decodes a Table with every number kept as the server sent it
// (json.Number), so no integer loses precision on the way to a cell.
func DecodeTable(raw []byte) (*metav1.Table, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var t metav1.Table
	if err := dec.Decode(&t); err != nil {
		return nil, err
	}
	return &t, nil
}

// TableColumns picks the columns Klyx draws from a table, in the server's
// order, with the index of each in a row's cells. Klyx draws the name and a
// live age itself: the name column (the first, format "name") is dropped,
// and so is the creation-time Age where that is proven - in a built-in
// group's table by its name, in a CRD's by its JSONPath. An Age whose source
// is unproven stays a column (design review 1).
func TableColumns(t *metav1.Table, builtin bool) ([]Column, []int) {
	keep := make([]int, 0, len(t.ColumnDefinitions))
	cols := make([]Column, 0, len(t.ColumnDefinitions))
	for i, d := range t.ColumnDefinitions {
		path := ""
		if strings.HasPrefix(d.Description, crdColumnDescription) {
			path = strings.TrimSpace(strings.TrimPrefix(d.Description, crdColumnDescription))
		}
		if i == 0 && d.Format == "name" {
			continue
		}
		if strings.EqualFold(d.Name, "age") && (path == ".metadata.creationTimestamp" || (builtin && path == "")) {
			continue
		}
		keep = append(keep, i)
		cols = append(cols, Column{
			Name: d.Name, Type: d.Type, Format: d.Format, Description: d.Description, JSONPath: path,
			Priority: d.Priority, Numeric: d.Type == "integer" || d.Type == "number",
			ConditionType: conditionTypeOf(path),
		})
	}
	return cols, keep
}

// RowCells renders a row's cells for the kept columns.
func RowCells(row metav1.TableRow, keep []int) []string {
	cells := make([]string, len(keep))
	for j, k := range keep {
		if k < len(row.Cells) {
			cells[j] = CellText(row.Cells[k])
		}
	}
	return cells
}

// FromTable turns a Table read with includeObject=Metadata into a page,
// with the columns TableColumns keeps.
func FromTable(t *metav1.Table, builtin bool) (InstancePage, error) {
	cols, keep := TableColumns(t, builtin)
	page := InstancePage{Continue: t.Continue, Columns: cols, Items: make([]InstanceMeta, 0, len(t.Rows))}
	for i, row := range t.Rows {
		var meta metav1.PartialObjectMetadata
		if len(row.Object.Raw) == 0 {
			return InstancePage{}, fmt.Errorf("table row %d carries no object metadata", i)
		}
		if err := json.Unmarshal(row.Object.Raw, &meta); err != nil {
			return InstancePage{}, fmt.Errorf("table row %d: %w", i, err)
		}
		page.Items = append(page.Items, InstanceMeta{
			Namespace: meta.Namespace,
			Name:      meta.Name,
			Created:   meta.CreationTimestamp.Time,
			Cells:     RowCells(row, keep),
		})
	}
	return page, nil
}

// CellText renders one decoded table cell. JSON numbers arrive as float64:
// a whole number prints without a fraction; null is empty; a list or map
// (a JSONPath that matched one) prints as compact JSON.
func CellText(v interface{}) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case json.Number:
		return x.String()
	case bool:
		return strconv.FormatBool(x)
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(x, 10)
	case int:
		return strconv.Itoa(x)
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return fmt.Sprint(x)
		}
		return string(b)
	}
}
