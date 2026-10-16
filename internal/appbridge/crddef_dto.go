package appbridge

import "github.com/moomora/klyx/internal/crd"

// CRDDefinitionDTO is one CRD read in full for the definition inspector
// (The Line slice 15c). Error marks a failed read: never an empty CRD.
type CRDDefinitionDTO struct {
	Name           string           `json:"name"`
	Group          string           `json:"group"`
	Kind           string           `json:"kind"`
	Plural         string           `json:"plural"`
	Singular       string           `json:"singular"`
	ListKind       string           `json:"listKind"`
	Scope          string           `json:"scope"`
	ShortNames     []string         `json:"shortNames"`
	Categories     []string         `json:"categories"`
	Operator       string           `json:"operator"`
	UID            string           `json:"uid"`
	Created        string           `json:"created"`
	Versions       []CRDVersionDTO  `json:"versions"`
	StoredVersions []string         `json:"storedVersions"`
	Conversion     CRDConversionDTO `json:"conversion"`
	Conditions     []ConditionDTO   `json:"conditions"`
	SchemaVersion  string           `json:"schemaVersion"`
	// SchemaRecorded: the version records a schema; Schema[0] is its root.
	SchemaRecorded bool             `json:"schemaRecorded"`
	Schema         []crd.SchemaNode `json:"schema"`
	// SchemaTruncated: the schema has more fields than the flattening keeps.
	SchemaTruncated bool   `json:"schemaTruncated,omitempty"`
	Error           string `json:"error,omitempty"`
	NotFound        bool   `json:"notFound,omitempty"`
}

// CRDVersionDTO is one version of a CRD.
type CRDVersionDTO struct {
	Name               string             `json:"name"`
	Served             bool               `json:"served"`
	Storage            bool               `json:"storage"`
	Deprecated         bool               `json:"deprecated"`
	DeprecationWarning string             `json:"deprecationWarning,omitempty"`
	Columns            []PrinterColumnDTO `json:"columns"`
}

// PrinterColumnDTO is one additionalPrinterColumn as the CRD declares it.
type PrinterColumnDTO struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Format      string `json:"format,omitempty"`
	JSONPath    string `json:"jsonPath"`
	Description string `json:"description,omitempty"`
	Priority    int64  `json:"priority"`
}

// CRDConversionDTO is spec.conversion as configured.
type CRDConversionDTO struct {
	Strategy       string   `json:"strategy"`
	URL            string   `json:"url,omitempty"`
	ServiceNS      string   `json:"serviceNamespace,omitempty"`
	ServiceName    string   `json:"serviceName,omitempty"`
	ServicePort    int64    `json:"servicePort,omitempty"`
	ServicePath    string   `json:"servicePath,omitempty"`
	ReviewVersions []string `json:"reviewVersions,omitempty"`
}

func definitionDTO(d crd.Definition) CRDDefinitionDTO {
	out := CRDDefinitionDTO{
		Name: d.Name, Group: d.Group, Kind: d.Kind, Plural: d.Plural, Singular: d.Singular, ListKind: d.ListKind, Scope: d.Scope,
		ShortNames: nonNil(d.ShortNames), Categories: nonNil(d.Categories), Operator: d.Operator, UID: d.UID, Created: rfc3339(d.Created),
		StoredVersions: nonNil(d.StoredVersions), SchemaVersion: d.SchemaVersion, SchemaRecorded: d.SchemaRecorded, Schema: d.Schema, SchemaTruncated: d.SchemaTruncated,
		Conversion: CRDConversionDTO{Strategy: d.Conversion.Strategy, URL: d.Conversion.URL, ServiceNS: d.Conversion.ServiceNS,
			ServiceName: d.Conversion.ServiceName, ServicePort: d.Conversion.ServicePort, ServicePath: d.Conversion.ServicePath,
			ReviewVersions: d.Conversion.ReviewVersions},
		Versions:   make([]CRDVersionDTO, 0, len(d.Versions)),
		Conditions: make([]ConditionDTO, 0, len(d.Conditions)),
	}
	if out.Schema == nil {
		out.Schema = []crd.SchemaNode{}
	}
	for _, v := range d.Versions {
		cols := make([]PrinterColumnDTO, 0, len(v.Columns))
		for _, c := range v.Columns {
			cols = append(cols, PrinterColumnDTO{Name: c.Name, Type: c.Type, Format: c.Format, JSONPath: c.JSONPath, Description: c.Description, Priority: c.Priority})
		}
		out.Versions = append(out.Versions, CRDVersionDTO{Name: v.Name, Served: v.Served, Storage: v.Storage, Deprecated: v.Deprecated, DeprecationWarning: v.DeprecationWarning, Columns: cols})
	}
	for _, c := range d.Conditions {
		out.Conditions = append(out.Conditions, ConditionDTO{Type: c.Type, Status: c.Status, Reason: c.Reason, Message: c.Message,
			LastTransition: rfc3339(c.LastTransition), ObservedGeneration: c.ObservedGeneration})
	}
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
