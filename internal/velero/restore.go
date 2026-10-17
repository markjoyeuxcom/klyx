package velero

import (
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The Restores kind (coverage S2b §1, §3): read on demand, never watched,
// and a fact without a verdict.
const (
	KindRestore      = "Restore"
	ResourceRestores = "restores"
)

// Restore projection limits (S2b §3): the CRD bounds neither the mapping
// nor its strings, so the lists are capped and then cut to fit MaxObject,
// their counts kept.
const (
	maxMapping           = 50
	maxRestoreNamespaces = 20
)

// MapEntry is one namespaceMapping entry: object names, else the invalid
// marker.
type MapEntry struct {
	From string `json:"f"`
	To   string `json:"t"`
}

// Restore is one projected Restore.
type Restore struct {
	Meta
	BackupName             string     `json:"bk,omitempty"`
	BackupNameInvalid      bool       `json:"bki,omitempty"`
	ScheduleName           string     `json:"sch,omitempty"`
	ScheduleNameInvalid    bool       `json:"schi,omitempty"`
	Included               []string   `json:"in,omitempty"`
	IncludedCount          int        `json:"inN,omitempty"`
	Excluded               []string   `json:"ex,omitempty"`
	ExcludedCount          int        `json:"exN,omitempty"`
	Mapping                []MapEntry `json:"map,omitempty"`
	MappingCount           int        `json:"mapN,omitempty"`
	ResourcesIncluded      int        `json:"riN,omitempty"`
	ResourcesExcluded      int        `json:"reN,omitempty"`
	LabelSelector          bool       `json:"sel,omitempty"`
	OrLabelSelectors       int        `json:"orSel,omitempty"`
	RestorePVs             *bool      `json:"pvs,omitempty"`
	ExistingResourcePolicy string     `json:"erp,omitempty"`
	ItemOperationTimeout   string     `json:"iot,omitempty"`
	Hooks                  int        `json:"hooks,omitempty"`
	ResourceModifier       string     `json:"mod,omitempty"`
	ResourcePolicy         string     `json:"pol,omitempty"`
	Phase                  string     `json:"phase,omitempty"`
	Start                  time.Time  `json:"st"`
	Completion             time.Time  `json:"co"`
	Errors                 int        `json:"err,omitempty"`
	Warnings               int        `json:"warn,omitempty"`
	ItemsTotal             int        `json:"it,omitempty"`
	ItemsRestored          int        `json:"ir,omitempty"`
	OperationsAttempted    int        `json:"oa,omitempty"`
	OperationsCompleted    int        `json:"oc,omitempty"`
	OperationsFailed       int        `json:"of,omitempty"`
	HooksAttempted         int        `json:"ha,omitempty"`
	HooksFailed            int        `json:"hf,omitempty"`
	Failure                *Class     `json:"fail,omitempty"`
	Validation             []Class    `json:"ve,omitempty"`
	ValidationCount        int        `json:"veN,omitempty"`
	// Cut: a list was cut to fit MaxObject; its count still says how many.
	Cut  bool `json:"cut,omitempty"`
	Size int  `json:"-"`
}

// configMapRef is a TypedLocalObjectReference's name when it names a
// ConfigMap (the only kind Velero reads there), never read by Klyx.
func configMapRef(u *unstructured.Unstructured, field string) string {
	if !strings.EqualFold(str(u, "spec", field, "kind"), "ConfigMap") {
		return ""
	}
	return clipName(str(u, "spec", field, "name"))
}

func nameOrInvalid(s string) (string, bool) {
	if s == "" {
		return "", false
	}
	if validName(s) {
		return s, false
	}
	return "", true
}

func nsOrInvalid(s string) string {
	if validNamespace(s) {
		return s
	}
	return "(invalid name)"
}

// ProjectRestore builds a Restore's projection. Hooks and selectors are
// counted, never shown; the resource modifier and policy ConfigMaps are
// named, never read (S2b §5). It never mutates u.
func ProjectRestore(u *unstructured.Unstructured) *Restore {
	r := &Restore{Meta: metaOf(u), Phase: disp(str(u, "status", "phase"), maxShort),
		Start: ts(u, "status", "startTimestamp"), Completion: ts(u, "status", "completionTimestamp"),
		Errors: num(u, "status", "errors"), Warnings: num(u, "status", "warnings"),
		ItemsTotal: num(u, "status", "progress", "totalItems"), ItemsRestored: num(u, "status", "progress", "itemsRestored"),
		OperationsAttempted: num(u, "status", "restoreItemOperationsAttempted"), OperationsCompleted: num(u, "status", "restoreItemOperationsCompleted"),
		OperationsFailed: num(u, "status", "restoreItemOperationsFailed"),
		HooksAttempted:   num(u, "status", "hookStatus", "hooksAttempted"), HooksFailed: num(u, "status", "hookStatus", "hooksFailed"),
		RestorePVs: boolp(u, "spec", "restorePVs"), ExistingResourcePolicy: disp(str(u, "spec", "existingResourcePolicy"), maxShort),
		ItemOperationTimeout: disp(str(u, "spec", "itemOperationTimeout"), maxShort),
		ResourceModifier:     configMapRef(u, "resourceModifier"), ResourcePolicy: configMapRef(u, "resourcePolicy")}
	r.BackupName, r.BackupNameInvalid = nameOrInvalid(str(u, "spec", "backupName"))
	r.ScheduleName, r.ScheduleNameInvalid = nameOrInvalid(str(u, "spec", "scheduleName"))
	r.Included, r.IncludedCount = names(strs(u, "spec", "includedNamespaces"), maxRestoreNamespaces, validNamespace)
	r.Excluded, r.ExcludedCount = names(strs(u, "spec", "excludedNamespaces"), maxRestoreNamespaces, validNamespace)
	r.ResourcesIncluded, r.ResourcesExcluded = len(strs(u, "spec", "includedResources")), len(strs(u, "spec", "excludedResources"))
	if m, ok, _ := unstructured.NestedMap(u.Object, "spec", "labelSelector"); ok && m != nil {
		r.LabelSelector = true
	}
	if sels, ok, _ := unstructured.NestedSlice(u.Object, "spec", "orLabelSelectors"); ok {
		r.OrLabelSelectors = len(sels)
	}
	if hooks, ok, _ := unstructured.NestedSlice(u.Object, "spec", "hooks", "resources"); ok {
		r.Hooks = len(hooks)
	}
	if m, ok, _ := unstructured.NestedMap(u.Object, "spec", "namespaceMapping"); ok {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		r.MappingCount = len(keys)
		for _, k := range keys {
			if len(r.Mapping) == maxMapping {
				break
			}
			to, _ := m[k].(string)
			r.Mapping = append(r.Mapping, MapEntry{From: nsOrInvalid(k), To: nsOrInvalid(to)})
		}
	}
	if msg := str(u, "status", "failureReason"); msg != "" {
		c, _ := Classify(FieldRestoreFailure, msg)
		c = bounded(c)
		r.Failure = &c
	}
	r.Validation, r.ValidationCount = classes(FieldRestoreValidation, strs(u, "status", "validationErrors"))
	r.fit()
	return r
}

// fit cuts the mapping, then the excluded, then the included namespaces
// from their tails until the projection is within MaxObject. Every other
// field is bounded on its own, so the lists alone can overflow.
func (r *Restore) fit() {
	r.Size = size(r)
	for r.Size > MaxObject {
		over := r.Size - MaxObject
		for over > 0 && (len(r.Mapping) > 0 || len(r.Excluded) > 0 || len(r.Included) > 0) {
			switch {
			case len(r.Mapping) > 0:
				e := r.Mapping[len(r.Mapping)-1]
				over -= len(e.From) + len(e.To) + len(`{"f":"","t":""},`)
				r.Mapping = r.Mapping[:len(r.Mapping)-1]
			case len(r.Excluded) > 0:
				over -= len(r.Excluded[len(r.Excluded)-1]) + len(`"",`)
				r.Excluded = r.Excluded[:len(r.Excluded)-1]
			default:
				over -= len(r.Included[len(r.Included)-1]) + len(`"",`)
				r.Included = r.Included[:len(r.Included)-1]
			}
		}
		r.Cut = true
		was := r.Size
		r.Size = size(r)
		if r.Size >= was {
			return // nothing left to cut
		}
	}
}

// ProjectRestores projects a list read, newest created first.
func ProjectRestores(items []unstructured.Unstructured) []*Restore {
	out := make([]*Restore, 0, len(items))
	for i := range items {
		out = append(out, ProjectRestore(&items[i]))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Created.Equal(out[j].Created) {
			return out[i].Created.After(out[j].Created)
		}
		return out[i].Namespace+"/"+out[i].Name < out[j].Namespace+"/"+out[j].Name
	})
	return out
}
