package crd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"

	metatable "k8s.io/apimachinery/pkg/api/meta/table"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/jsonpath"

	"github.com/moomora/klyx/internal/urlscrub"
)

// Local cells for the registered payload kinds (coverage S0b §3, revision
// 3): the server's cell text is never released for them. The list asks for
// whole objects, each is sanitised first, and every printer column's
// JSONPath is evaluated here over the sanitised copy - so a column
// definition that changed between reads can at worst show a wrong value,
// never protected data. The definition chooses presentation only.

// payloadResources maps "group/plural" to the registered payload kinds, so
// a list can ask for objects before it reads them.
var payloadResources = map[string]bool{
	"fluxcd.controlplane.io/resourcesets":              true,
	"fluxcd.controlplane.io/resourcesetinputproviders": true,
	"fluxcd.controlplane.io/fluxinstances":             true,
	"kustomize.toolkit.fluxcd.io/kustomizations":       true,
	"helm.toolkit.fluxcd.io/helmreleases":              true,
	"argoproj.io/applications":                         true,
	"argoproj.io/applicationsets":                      true,
}

// IsPayloadResource reports whether group/plural is a registered payload
// kind.
func IsPayloadResource(group, plural string) bool { return payloadResources[group+"/"+plural] }

// LocalColumns turns a version's printer columns into the columns Klyx
// draws: the creation-time Age is dropped as TableColumns does.
func LocalColumns(defs []PrinterColumn) []Column {
	out := make([]Column, 0, len(defs))
	for _, d := range defs {
		if d.JSONPath == ".metadata.creationTimestamp" && d.Name == "Age" {
			continue
		}
		desc := d.Description
		if desc == "" {
			desc = crdColumnDescription + d.JSONPath
		}
		out = append(out, Column{
			Name: d.Name, Type: d.Type, Format: d.Format, Description: desc, JSONPath: d.JSONPath,
			Priority: int32(d.Priority), Numeric: d.Type == "integer" || d.Type == "number",
			ConditionType: conditionTypeOf(d.JSONPath),
		})
	}
	return out
}

// LocalCells evaluates every column over a sanitised object, as the API
// server's CRD table convertor does (the first result; strings printed by
// the JSONPath printer, other types coerced). A column that does not
// evaluate is empty.
func LocalCells(cols []Column, sanitized map[string]any) []string {
	cells := make([]string, len(cols))
	var buf bytes.Buffer
	for i, col := range cols {
		jp := jsonpath.New(col.Name)
		jp.AllowMissingKeys(true)
		if err := jp.Parse(fmt.Sprintf("{%s}", col.JSONPath)); err != nil {
			continue
		}
		results, err := jp.FindResults(sanitized)
		if err != nil || len(results) == 0 || len(results[0]) == 0 {
			continue
		}
		value := results[0][0].Interface()
		if col.Type == "string" {
			buf.Reset()
			if err := jp.PrintResults(&buf, []reflect.Value{reflect.ValueOf(value)}); err == nil {
				cells[i] = urlscrub.Text(buf.String()).Text
			}
			continue
		}
		cells[i] = CellText(localCell(col.Type, value))
	}
	return cells
}

// localCell is the server's cellForJSONValue.
func localCell(typ string, value any) any {
	if value == nil {
		return nil
	}
	switch typ {
	case "integer":
		switch v := value.(type) {
		case int64:
			return v
		case float64:
			return int64(v)
		case json.Number:
			if i, err := v.Int64(); err == nil {
				return i
			}
		}
	case "number":
		switch v := value.(type) {
		case int64:
			return float64(v)
		case float64:
			return v
		case json.Number:
			if f, err := v.Float64(); err == nil {
				return f
			}
		}
	case "boolean":
		if b, ok := value.(bool); ok {
			return b
		}
	case "date":
		if s, ok := value.(string); ok {
			var ts metav1.Time
			if err := ts.UnmarshalQueryParameter(s); err != nil {
				return "<invalid>"
			}
			return metatable.ConvertToHumanReadableDateType(ts)
		}
	}
	return nil
}

// LocalPage builds a page from a Table read with includeObject=Object for a
// registered payload kind. The server's cells are discarded unread; cols
// may be nil (no definition), and then the rows carry no cells.
func LocalPage(t *metav1.Table, cols []Column) (InstancePage, error) {
	page := InstancePage{Continue: t.Continue, Items: make([]InstanceMeta, 0, len(t.Rows))}
	if cols != nil {
		page.Columns = cols
	}
	for i, row := range t.Rows {
		if len(row.Object.Raw) == 0 {
			return InstancePage{}, fmt.Errorf("table row %d carries no object", i)
		}
		u := &unstructured.Unstructured{}
		if err := u.UnmarshalJSON(row.Object.Raw); err != nil {
			return InstancePage{}, fmt.Errorf("table row %d: %w", i, err)
		}
		item := InstanceMeta{Namespace: u.GetNamespace(), Name: u.GetName(), Created: u.GetCreationTimestamp().Time}
		if cols != nil {
			s := SanitizeEmbedded(u.GroupVersionKind().Group, u.GetKind(), u.Object)
			item.Cells = LocalCells(cols, s.Object)
		}
		page.Items = append(page.Items, item)
	}
	return page, nil
}

// LocalSummary is the detail's Summary for a registered payload kind, from
// its own sanitised object.
func LocalSummary(cols []Column, sanitized map[string]any) []SummaryCell {
	cells := LocalCells(cols, sanitized)
	out := make([]SummaryCell, 0, len(cols))
	for i, col := range cols {
		out = append(out, SummaryCell{Column: col, Value: cells[i]})
	}
	return out
}

// ScrubCells applies the URL rule to every cell of a page (every kind).
func ScrubCells(page *InstancePage) {
	for i := range page.Items {
		for j, c := range page.Items[i].Cells {
			page.Items[i].Cells[j] = urlscrub.Text(c).Text
		}
	}
}
